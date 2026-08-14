// Package config loads gerry serve's configuration file.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the gerry serve configuration.
type Config struct {
	DB  string `yaml:"db"`
	API struct {
		Listen string `yaml:"listen"`  // default 127.0.0.1:4780
		KeyEnv string `yaml:"key_env"` // env var holding the API key (default GERRY_API_KEY)
		// MetricsListen, when set, serves /metrics on its own listener and
		// REMOVES it from the API mux — so a public ingress in front of the
		// API cannot expose metrics. Point your scraper here (e.g. ":9091").
		MetricsListen string `yaml:"metrics_listen"`
		// AllowUnauthenticated permits a non-loopback listen with no API key.
		// Without it, gerry refuses to serve an open registry off-loopback.
		AllowUnauthenticated bool `yaml:"allow_unauthenticated"`
	} `yaml:"api"`
	Zones []ZoneConfig `yaml:"zones"`
	Proxy struct {
		Enabled       bool   `yaml:"enabled"`
		HTTP          string `yaml:"http"` // ":80"; empty disables redirect listener
		TLS           string `yaml:"tls"`  // ":443"
		ExtraTLSPorts []int  `yaml:"extra_tls_ports"`
		CACert        string `yaml:"ca_cert"` // empty → auto CA under data dir
		CAKey         string `yaml:"ca_key"`
		CADir         string `yaml:"ca_dir"` // used when cert/key unset
	} `yaml:"proxy"`
	DNS struct {
		Enabled bool     `yaml:"enabled"`
		Listen  string   `yaml:"listen"` // "127.0.0.1:5353"
		Zones   []string `yaml:"zones"`  // TLDs or zones to answer for
		// Advertise sets the IP that answers carry. Empty = loopback.
		// "tailscale" resolves this machine's tailnet IPv4 at startup, so
		// tailnet peers with split DNS routed here reach your dev hostnames.
		// Any literal IP works too. Remember to make Listen reachable from
		// the peers (e.g. ":53") when advertising a non-loopback address.
		Advertise string `yaml:"advertise"`
	} `yaml:"dns"`
	Supervise bool `yaml:"supervise"`
	// DockerLabels auto-claims hostnames for containers labeled
	// gerrymander.hostname=... (compose-label pattern). nil = auto: on
	// whenever the proxy runs and the docker CLI is present.
	DockerLabels struct {
		Enabled  *bool         `yaml:"enabled"`
		Interval time.Duration `yaml:"interval"` // default 5s
	} `yaml:"docker_labels"`
	Observer  struct {
		Enabled      bool          `yaml:"enabled"`
		Zones        []string      `yaml:"zones"`
		AutoRegister bool          `yaml:"auto_register"`
		Interval     time.Duration `yaml:"interval"`
		APIServer    string        `yaml:"api_server"` // empty → in-cluster
		TokenFile    string        `yaml:"token_file"`
		CAFile       string        `yaml:"ca_file"`
		Insecure     bool          `yaml:"insecure"`
	} `yaml:"observer"`
	// Actuator writes Traefik IngressRoutes for registry claims that carry
	// service backends. Off by default; it shares the observer's cluster
	// credentials.
	Actuator struct {
		Enabled bool `yaml:"enabled"`
		// Provider: "traefik-crd" (default) writes Traefik IngressRoutes;
		// "gateway-api" writes Gateway API HTTPRoutes attached to Gateway.
		Provider    string        `yaml:"provider"`
		Zones       []string      `yaml:"zones"`
		EntryPoints []string      `yaml:"entry_points"` // traefik-crd only
		Interval    time.Duration `yaml:"interval"`
		Gateway     struct {      // gateway-api only
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"gateway"`
	} `yaml:"actuator"`
	// CRDIngest feeds HostnameReservation CRs into the registry (GitOps
	// input; the database stays truth). Uses the observer's cluster creds.
	CRDIngest struct {
		Enabled  bool          `yaml:"enabled"`
		Interval time.Duration `yaml:"interval"`
	} `yaml:"crd_ingest"`
	// NginxSync renders active allocations with address backends into ONE
	// marker-tagged nginx include file and reloads — for machines where
	// nginx is the dataplane. Files without gerry's marker are never
	// overwritten.
	NginxSync struct {
		Enabled   bool          `yaml:"enabled"`
		ConfPath  string        `yaml:"conf_path"`  // e.g. /opt/homebrew/etc/nginx/servers/gerry.conf
		Listen    string        `yaml:"listen"`     // default "80"
		ReloadCmd string        `yaml:"reload_cmd"` // default "nginx -s reload"; "" = write only
		Interval  time.Duration `yaml:"interval"`
	} `yaml:"nginx_sync"`
	// NPMSync drives an Nginx Proxy Manager instance over its REST API:
	// active allocations with address backends become proxy hosts (with a
	// marker in advanced_config; UI-made hosts are never touched).
	NPMSync struct {
		Enabled     bool          `yaml:"enabled"`
		URL         string        `yaml:"url"`          // e.g. http://100.71.144.24:81
		IdentityEnv string        `yaml:"identity_env"` // default NPM_IDENTITY
		SecretEnv   string        `yaml:"secret_env"`   // default NPM_SECRET
		LocalHost   string        `yaml:"local_host"`   // "@local" maps here; default host.docker.internal
		Zones       []string      `yaml:"zones"`
		Interval    time.Duration `yaml:"interval"`
	} `yaml:"npm_sync"`
	// DNSSync reconciles per-label records at a DNS provider (experimental;
	// cloudflare only). Records carry the comment "gerrymander-managed" and
	// only commented records are ever touched.
	DNSSync struct {
		Enabled     bool   `yaml:"enabled"`
		Provider    string `yaml:"provider"`      // "cloudflare"
		APITokenEnv string `yaml:"api_token_env"` // default CLOUDFLARE_API_TOKEN
		Zones       []struct {
			Zone     string `yaml:"zone"`
			CFZoneID string `yaml:"cf_zone_id"`
			Target   string `yaml:"target"` // CNAME target or A-record IP
			Proxied  bool   `yaml:"proxied"`
		} `yaml:"zones"`
		Interval time.Duration `yaml:"interval"`
	} `yaml:"dns_sync"`
	Ports struct {
		EnsureDefaultPool bool `yaml:"ensure_default_pool"`
	} `yaml:"ports"`
	HoldTTL time.Duration `yaml:"hold_ttl"`
}

// ZoneConfig declares a zone to ensure at startup.
type ZoneConfig struct {
	Name    string `yaml:"name"`
	Profile string `yaml:"profile"` // prod | dev
}

// Load reads a config file and applies defaults.
func Load(path string) (*Config, error) {
	var c Config
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	c.applyDefaults()
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.DB == "" {
		home, _ := os.UserHomeDir()
		c.DB = home + "/.gerrymander/gerry.db"
	}
	if c.API.Listen == "" {
		c.API.Listen = "127.0.0.1:4780"
	}
	if c.API.KeyEnv == "" {
		c.API.KeyEnv = "GERRY_API_KEY"
	}
	if c.DNS.Listen == "" {
		c.DNS.Listen = "127.0.0.1:5353"
	}
	if c.Observer.Interval <= 0 {
		c.Observer.Interval = time.Minute
	}
	if c.HoldTTL <= 0 {
		c.HoldTTL = 15 * time.Minute
	}
	if c.Proxy.Enabled && c.Proxy.TLS == "" {
		c.Proxy.TLS = ":443"
	}
}
