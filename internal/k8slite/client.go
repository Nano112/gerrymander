// Package k8slite is the deliberately tiny Kubernetes API client shared by
// the observer (read) and the actuator (write). No client-go: a bearer
// token, a CA, and JSON over HTTP is all these controllers need.
package k8slite

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Config points at a cluster. The zero value + InCluster() covers pods;
// out-of-cluster runs set APIServer/TokenFile explicitly.
type Config struct {
	APIServer string
	TokenFile string
	Token     string // literal token (tests); TokenFile wins when set
	CAFile    string
	Insecure  bool
}

// InCluster returns the standard service-account config.
func InCluster() (Config, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" {
		return Config{}, errors.New("not in cluster")
	}
	return Config{
		APIServer: "https://" + host + ":" + port,
		TokenFile: "/var/run/secrets/kubernetes.io/serviceaccount/token",
		CAFile:    "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
	}, nil
}

// Client performs typed requests against the API server.
type Client struct {
	Cfg  Config
	http *http.Client
}

// New builds a client (TLS material loaded lazily on first use is avoided:
// fail fast here).
func New(cfg Config) (*Client, error) {
	tlsCfg := &tls.Config{InsecureSkipVerify: cfg.Insecure}
	if cfg.CAFile != "" {
		ca, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(ca)
		tlsCfg.RootCAs = pool
	}
	return &Client{Cfg: cfg, http: &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}}}, nil
}

// StatusError carries the API server's HTTP status for callers that branch
// on 404/409.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string { return fmt.Sprintf("k8s %d: %s", e.Code, e.Body) }

// IsNotFound reports a 404 StatusError.
func IsNotFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == 404
}

// Do performs a JSON request. body and into may be nil.
func (c *Client) Do(ctx context.Context, method, path string, body, into any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Cfg.APIServer+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	tok := c.Cfg.Token
	if c.Cfg.TokenFile != "" {
		if b, err := os.ReadFile(c.Cfg.TokenFile); err == nil {
			tok = strings.TrimSpace(string(b))
		}
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	if into != nil {
		return json.NewDecoder(resp.Body).Decode(into)
	}
	return nil
}
