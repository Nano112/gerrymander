// Package rexlink maps links printed on a Rex host to a viewer's local HTTPS zone.
package rexlink

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nano112/gerrymander/internal/core"
)

type Host struct {
	Server     string `json:"server"`
	Session    string `json:"session,omitempty"`
	RemoteZone string `json:"remote_zone"`
	LocalZone  string `json:"local_zone"`
	ProxyPort  int    `json:"proxy_port"`
	CACert     string `json:"ca_cert"`
	Rex        string `json:"rex,omitempty"`
}

type Hosts map[string]Host

func Load() (Hosts, error) {
	path := os.Getenv("GERRY_REX_HOSTS")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".gerrymander", "rex-hosts.json")
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) && os.Getenv("GERRY_REX_HOSTS") == "" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hosts Hosts
	if err = json.Unmarshal(b, &hosts); err != nil {
		return nil, err
	}
	zones := make(map[string]bool)
	for key, h := range hosts {
		if !validName(key) || !validName(h.LocalZone) || !validName(h.RemoteZone) || h.Server == "" || h.ProxyPort < 1 || h.ProxyPort > 65535 || h.CACert == "" {
			return nil, fmt.Errorf("invalid Rex host configuration for %q", key)
		}
		for zone := range zones {
			if inZone(h.LocalZone, zone) || inZone(zone, h.LocalZone) {
				return nil, fmt.Errorf("overlapping local Rex zones %s and %s", zone, h.LocalZone)
			}
		}
		zones[h.LocalZone] = true
	}
	return hosts, nil
}

func validName(name string) bool {
	n, err := core.Normalize(name, core.KindPlatform)
	return err == nil && n == name && n != "@" && !strings.Contains(n, "*") && len(n) <= 253
}

func inZone(name, zone string) bool { return name == zone || strings.HasSuffix(name, "."+zone) }

func TerminalLink(key, host string) string {
	display := "https://" + host
	if key == "" || !validName(key) || !validName(host) {
		return display
	}
	return "\x1b]8;;gerry://" + key + "/" + host + "/\x1b\\" + display + "\x1b]8;;\x1b\\"
}

// BrowserURL accepts only configured aliases and names in their remote zone.
// Using EscapedPath keeps encoded slashes and URL delimiters in the resource path.
func (hosts Hosts) BrowserURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "gerry" || u.User != nil {
		return nil, fmt.Errorf("invalid Gerry URL")
	}
	h, ok := hosts[u.Host]
	name, path, _ := strings.Cut(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if !ok || !validName(name) || !inZone(name, h.RemoteZone) {
		return nil, fmt.Errorf("unconfigured Rex link for %q", name)
	}
	u.Scheme = "https"
	u.Host = strings.TrimSuffix(name, h.RemoteZone) + h.LocalZone
	u.RawPath = "/" + path
	u.Path, err = url.PathUnescape(u.RawPath)
	return u, err
}
