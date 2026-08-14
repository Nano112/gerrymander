package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Nano112/gerrymander/internal/client"
	"github.com/Nano112/gerrymander/internal/core"
)

// Settings persist at ~/.gerrymander/gui.json.
type Settings struct {
	API        string `json:"api"`
	APIKey     string `json:"api_key"`
	ComposeDir string `json:"compose_dir"` // deploy/dev directory for the local daemon
}

func defaultSettings() Settings {
	home, _ := os.UserHomeDir()
	s := Settings{
		API:        "http://127.0.0.1:4780",
		ComposeDir: filepath.Join(home, "Documents", "code", "gerrymander", "deploy", "dev"),
	}
	if v := os.Getenv("GERRY_API"); v != "" {
		s.API = v
	}
	if v := os.Getenv("GERRY_API_KEY"); v != "" {
		s.APIKey = v
	}
	return s
}

func settingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gerrymander", "gui.json")
}

// App is the Wails-bound backend.
type App struct {
	ctx      context.Context
	settings Settings
}

// NewApp builds the backend, loading settings if present.
func NewApp() *App {
	a := &App{settings: defaultSettings()}
	if b, err := os.ReadFile(settingsPath()); err == nil {
		var s Settings
		if json.Unmarshal(b, &s) == nil {
			if s.API != "" {
				a.settings.API = s.API
			}
			if s.APIKey != "" {
				a.settings.APIKey = s.APIKey
			}
			if s.ComposeDir != "" {
				a.settings.ComposeDir = s.ComposeDir
			}
		}
	}
	return a
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

func (a *App) api() *client.Client { return client.New(a.settings.API, a.settings.APIKey) }

// GetSettings returns current settings (key masked).
func (a *App) GetSettings() Settings {
	s := a.settings
	if s.APIKey != "" {
		s.APIKey = "••••"
	}
	return s
}

// SaveSettings persists new settings ("••••" keeps the existing key).
func (a *App) SaveSettings(s Settings) error {
	if s.APIKey == "••••" {
		s.APIKey = a.settings.APIKey
	}
	a.settings = s
	os.MkdirAll(filepath.Dir(settingsPath()), 0o700)
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(settingsPath(), b, 0o600)
}

// Status is the header summary.
type Status struct {
	APIReachable bool   `json:"api_reachable"`
	APIBase      string `json:"api_base"`
	Zones        int    `json:"zones"`
	Allocations  int    `json:"allocations"`
	Conflicts    int    `json:"conflicts"`
	DaemonError  string `json:"daemon_error,omitempty"`
}

// GetStatus probes the daemon.
func (a *App) GetStatus() Status {
	st := Status{APIBase: a.settings.API}
	var zones struct {
		Zones []core.Zone `json:"zones"`
	}
	if err := a.api().Do(a.ctx, "GET", "/v1/zones", nil, &zones); err != nil {
		st.DaemonError = err.Error()
		return st
	}
	st.APIReachable = true
	st.Zones = len(zones.Zones)
	var allocs struct {
		Allocations []core.Allocation `json:"allocations"`
	}
	if a.api().Do(a.ctx, "GET", "/v1/allocations", nil, &allocs) == nil {
		st.Allocations = len(allocs.Allocations)
	}
	var conf struct {
		Conflicts []map[string]any `json:"conflicts"`
	}
	if a.api().Do(a.ctx, "GET", "/v1/conflicts", nil, &conf) == nil {
		st.Conflicts = len(conf.Conflicts)
	}
	return st
}

// TreeNode is one row of the district tree.
type TreeNode struct {
	ID       int64      `json:"id,omitempty"`
	Label    string     `json:"label"`
	FQDN     string     `json:"fqdn"`
	Kind     string     `json:"kind,omitempty"`
	State    string     `json:"state,omitempty"`
	Source   string     `json:"source,omitempty"`
	Owner    string     `json:"owner,omitempty"`
	Project  string     `json:"project,omitempty"`
	Wildcard bool       `json:"wildcard,omitempty"`
	Routes   []string   `json:"routes,omitempty"` // human-readable "443 → host:port"
	Children []TreeNode `json:"children,omitempty"`
}

// GetTree returns zones → allocations, nesting multi-level labels so
// "flow.schemati.test" sits under "schemati.test".
func (a *App) GetTree() ([]TreeNode, error) {
	var zones struct {
		Zones []core.Zone `json:"zones"`
	}
	if err := a.api().Do(a.ctx, "GET", "/v1/zones", nil, &zones); err != nil {
		return nil, err
	}
	var out []TreeNode
	for _, z := range zones.Zones {
		var allocs struct {
			Allocations []core.Allocation `json:"allocations"`
		}
		if err := a.api().Do(a.ctx, "GET", "/v1/allocations?zone="+z.Name, nil, &allocs); err != nil {
			return nil, err
		}
		zoneNode := TreeNode{Label: z.Name, FQDN: z.Name, Kind: "zone", Source: z.Profile}
		// Sort: apex first, then alphabetically; wildcards after their base.
		items := allocs.Allocations
		sort.Slice(items, func(i, j int) bool {
			if (items[i].Label == "@") != (items[j].Label == "@") {
				return items[i].Label == "@"
			}
			return items[i].Label < items[j].Label
		})
		for _, al := range items {
			node := allocNode(al)
			attachNode(&zoneNode, node, strings.TrimPrefix(al.Label, "*."))
		}
		out = append(out, zoneNode)
	}
	return out, nil
}

func allocNode(al core.Allocation) TreeNode {
	n := TreeNode{
		ID: al.ID, Label: al.Label, FQDN: al.FQDN, Kind: string(al.Kind),
		State: string(al.State), Source: string(al.Source), Owner: al.OwnerRef,
		Project:  al.Project,
		Wildcard: al.Spec.Wildcard || strings.HasPrefix(al.Label, "*."),
	}
	for _, r := range al.Spec.Routes {
		listen := "443"
		if r.Listen != 0 {
			listen = fmt.Sprintf("%d", r.Listen)
		}
		switch r.Backend.Kind {
		case "address":
			if ad := r.Backend.Address; ad != nil {
				n.Routes = append(n.Routes, fmt.Sprintf("%s → %s:%d", listen, ad.Host, ad.Port))
			}
		case "supervised":
			if s := r.Backend.Supervised; s != nil {
				n.Routes = append(n.Routes, fmt.Sprintf("%s → supervised: %s", listen, s.Cmd))
			}
		case "service":
			if s := r.Backend.Service; s != nil {
				n.Routes = append(n.Routes, fmt.Sprintf("%s → svc %s/%s:%d", listen, s.Namespace, s.Name, s.Port))
			}
		}
	}
	return n
}

// attachNode nests multi-level labels ("flow" under an existing parent chain
// when one exists, else directly under the zone).
func attachNode(zone *TreeNode, node TreeNode, label string) {
	if label == "@" || !strings.Contains(label, ".") {
		zone.Children = append(zone.Children, node)
		return
	}
	parts := strings.SplitN(label, ".", 2)
	parentLabel := parts[1]
	for i := range zone.Children {
		base := strings.TrimPrefix(zone.Children[i].Label, "*.")
		if base == parentLabel {
			zone.Children[i].Children = append(zone.Children[i].Children, node)
			return
		}
	}
	zone.Children = append(zone.Children, node)
}

// ClaimInput is the GUI claim form.
type ClaimInput struct {
	Zone     string `json:"zone"`
	Label    string `json:"label"`
	Kind     string `json:"kind"` // default platform for dev routing
	Wildcard bool   `json:"wildcard"`
	Backend  string `json:"backend"` // "host:port"
	Listen   []int  `json:"listen"`  // empty = default 443/80
	Owner    string `json:"owner"`
}

// Claim creates an allocation with routes.
func (a *App) Claim(in ClaimInput) error {
	if in.Kind == "" {
		in.Kind = "platform"
	}
	spec := map[string]any{"wildcard": in.Wildcard}
	if in.Backend != "" {
		host, port := splitHostPort(in.Backend)
		listens := in.Listen
		if len(listens) == 0 {
			listens = []int{0}
		}
		var routes []map[string]any
		for _, l := range listens {
			routes = append(routes, map[string]any{
				"listen":  l,
				"backend": map[string]any{"kind": "address", "address": map[string]any{"host": host, "port": port}},
			})
		}
		spec["routes"] = routes
	}
	return a.api().Do(a.ctx, "POST", "/v1/claims", map[string]any{
		"zone": in.Zone, "label": in.Label, "kind": in.Kind, "source": "manifest",
		"owner_ref": in.Owner, "spec": spec,
	}, nil)
}

// Release frees an allocation.
func (a *App) Release(id int64) error {
	return a.api().Do(a.ctx, "DELETE", fmt.Sprintf("/v1/allocations/%d", id), nil, nil)
}

// Availability proxies the availability check for the claim form.
func (a *App) Availability(zone, label string) (map[string]any, error) {
	var out map[string]any
	err := a.api().Do(a.ctx, "GET", "/v1/zones/"+zone+"/availability?label="+label, nil, &out)
	return out, err
}

// PortsView combines registry grants with live listeners.
type PortsView struct {
	Grants    []core.PortAllocation `json:"grants"`
	Listeners []Listener            `json:"listeners"`
}

// GetPorts returns grants + live listening sockets, cross-referenced.
func (a *App) GetPorts() (PortsView, error) {
	var v PortsView
	var grants struct {
		Ports []core.PortAllocation `json:"ports"`
	}
	// Daemon may be down; listeners still work.
	a.api().Do(a.ctx, "GET", "/v1/ports", nil, &grants)
	v.Grants = grants.Ports
	byPort := map[int]string{}
	for _, g := range grants.Ports {
		byPort[g.Value] = g.OwnerRef
	}
	ls, err := ScanListeners(a.ctx)
	if err != nil {
		return v, err
	}
	for i := range ls {
		ls[i].GerryOwner = byPort[ls[i].Port]
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].Port < ls[j].Port })
	v.Listeners = ls
	return v, nil
}

// KillPort terminates the process behind a listener.
func (a *App) KillPort(pid int, force bool) error { return KillProcess(pid, force) }

// GetProcesses lists supervised processes.
func (a *App) GetProcesses() ([]map[string]any, error) {
	var out struct {
		Processes []map[string]any `json:"processes"`
	}
	err := a.api().Do(a.ctx, "GET", "/v1/processes", nil, &out)
	return out.Processes, err
}

// StartProcess / StopProcess control supervised services.
func (a *App) StartProcess(name string) error {
	return a.api().Do(a.ctx, "POST", "/v1/processes/"+name+"/start", nil, nil)
}

func (a *App) StopProcess(name string) error {
	return a.api().Do(a.ctx, "POST", "/v1/processes/"+name+"/stop", nil, nil)
}

// ProcessLogs tails a supervised process's captured output.
func (a *App) ProcessLogs(name string, lines int) ([]string, error) {
	var out struct {
		Lines []string `json:"lines"`
	}
	err := a.api().Do(a.ctx, "GET", fmt.Sprintf("/v1/processes/%s/logs?lines=%d", name, lines), nil, &out)
	return out.Lines, err
}

// OpenURL opens a hostname in the default browser.
func (a *App) OpenURL(url string) { runtime.BrowserOpenURL(a.ctx, url) }

// DaemonUp / DaemonDown control the local dev-proxy container.
func (a *App) DaemonUp() (string, error)   { return a.compose("up", "-d") }
func (a *App) DaemonDown() (string, error) { return a.compose("down") }

func (a *App) compose(args ...string) (string, error) {
	if a.settings.ComposeDir == "" {
		return "", fmt.Errorf("compose_dir not configured")
	}
	cmd := exec.CommandContext(a.ctx, "docker", append([]string{"compose"}, args...)...)
	cmd.Dir = a.settings.ComposeDir
	// docker Desktop's CLI lives outside the GUI-app PATH.
	cmd.Env = append(os.Environ(), "PATH="+os.Getenv("PATH")+":/usr/local/bin:/opt/homebrew/bin")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func splitHostPort(s string) (string, int) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return s, 80
	}
	var port int
	fmt.Sscanf(s[i+1:], "%d", &port)
	if port == 0 {
		port = 80
	}
	return s[:i], port
}
