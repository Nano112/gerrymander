package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CA mints leaf certificates for proxied hostnames. It can load an existing
// root (e.g. the Caddy local CA already trusted on a dev machine) or generate
// its own.
type CA struct {
	cert *x509.Certificate
	key  any // crypto.Signer

	mu    sync.Mutex
	cache map[string]*tls.Certificate
}

// LoadCA reads a PEM cert+key pair.
func LoadCA(certPath, keyPath string) (*CA, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM block", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, fmt.Errorf("%s: no PEM block", keyPath)
	}
	var key any
	switch kb.Type {
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(kb.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(kb.Bytes)
	default:
		key, err = x509.ParsePKCS8PrivateKey(kb.Bytes)
	}
	if err != nil {
		return nil, fmt.Errorf("parse CA key: %w", err)
	}
	return &CA{cert: cert, key: key, cache: map[string]*tls.Certificate{}}, nil
}

// EnsureCA loads the CA at dir, generating one if absent.
func EnsureCA(dir string) (*CA, error) {
	certPath := filepath.Join(dir, "root.crt")
	keyPath := filepath.Join(dir, "root.key")
	if _, err := os.Stat(certPath); err == nil {
		return LoadCA(certPath, keyPath)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "gerrymander local CA", Organization: []string{"gerrymander"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	certOut := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kb, _ := x509.MarshalECPrivateKey(key)
	keyOut := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	if err := os.WriteFile(certPath, certOut, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, keyOut, 0o600); err != nil {
		return nil, err
	}
	return LoadCA(certPath, keyPath)
}

// Leaf returns a certificate for host, minting and caching on first use.
func (c *CA) Leaf(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if crt, ok := c.cache[host]; ok {
		// Reissue when within a day of expiry.
		if leaf, err := x509.ParseCertificate(crt.Certificate[0]); err == nil && time.Until(leaf.NotAfter) > 24*time.Hour {
			return crt, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(0, 3, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	crt := &tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: key}
	c.cache[host] = crt
	return crt, nil
}

// TLSConfig returns a config that mints per-SNI leaves.
func (c *CA) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			host := hello.ServerName
			if host == "" {
				host = "localhost"
			}
			return c.Leaf(host)
		},
	}
}

// RootPEM returns the CA certificate PEM (for trust installation docs).
func (c *CA) RootPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.cert.Raw})
}
