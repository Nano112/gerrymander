// gerry is the gerrymander CLI: server, client commands, manifest apply, MCP.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Nano112/gerrymander/internal/api"
	"github.com/Nano112/gerrymander/internal/client"
	"github.com/Nano112/gerrymander/internal/config"
	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/dnsserver"
	"github.com/Nano112/gerrymander/internal/manifest"
	"github.com/Nano112/gerrymander/internal/mcp"
	"github.com/Nano112/gerrymander/internal/observe"
	"github.com/Nano112/gerrymander/internal/proxy"
	"github.com/Nano112/gerrymander/internal/service"
	"github.com/Nano112/gerrymander/internal/store"
	"github.com/Nano112/gerrymander/internal/supervise"
)

var version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "serve":
		err = cmdServe(args)
	case "claim":
		err = cmdClaim(args)
	case "port":
		err = cmdPort(args)
	case "avail":
		err = cmdAvail(args)
	case "ls":
		err = cmdLs(args)
	case "release":
		err = cmdRelease(args)
	case "rename":
		err = cmdRename(args)
	case "zone":
		err = cmdZone(args)
	case "conflicts":
		err = cmdConflicts(args)
	case "up":
		err = cmdUp(args)
	case "down":
		err = cmdDown(args)
	case "mcp":
		err = cmdMCP(args)
	case "ca-export":
		err = cmdCAExport(args)
	case "version":
		fmt.Println("gerry", version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gerry:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `gerrymander — hostname and port control plane

usage: gerry <command> [flags]

server:
  serve --config <file>        run API (+ proxy/DNS/observer per config)
  ca-export --dir <dir>        print the local CA root certificate PEM

client (env: GERRY_API, GERRY_API_KEY):
  claim  --zone Z --label L [--owner O] [--kind tenant|platform] [--hold]
  port   --owner O [--pool dev] [-q]
  zone   add --name Z [--profile dev|prod]
  avail  --zone Z --label L
  ls     [--zone Z] [--owner O]
  release --id N
  rename --id N --label NEW       atomic; keeps id/owner/routes/history
  conflicts
  up     [-f gerrymander.yaml]  apply a project manifest
  down   [-f gerrymander.yaml]  release a project manifest
  mcp                           serve MCP over stdio
`)
}

func apiClient() *client.Client {
	base := os.Getenv("GERRY_API")
	if base == "" {
		base = "http://127.0.0.1:4780"
	}
	return client.New(base, os.Getenv("GERRY_API_KEY"))
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

// --- serve ---

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", "", "config file (yaml)")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(log)

	if err := os.MkdirAll(filepath.Dir(cfg.DB), 0o755); err != nil {
		return err
	}
	st, err := store.Open("sqlite:" + cfg.DB)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	for _, z := range cfg.Zones {
		if _, err := st.EnsureZone(ctx, core.Zone{Name: z.Name, Profile: z.Profile}); err != nil {
			return fmt.Errorf("zone %s: %w", z.Name, err)
		}
	}

	ports := service.NewPorts(st)
	if cfg.Ports.EnsureDefaultPool {
		if err := ports.EnsureDefaultPool(ctx); err != nil {
			return err
		}
	}
	alloc := service.NewAlloc(st, ports)
	alloc.DefaultHoldTTL = cfg.HoldTTL
	go alloc.RunReaper(ctx, 30*time.Second)

	var sup *supervise.Manager
	if cfg.Supervise {
		sup = supervise.NewManager(ports)
		go sup.RunIdleSweeper(ctx)
		defer sup.StopAll()
	}

	var obs *observe.Observer
	if cfg.Observer.Enabled {
		k8s := observe.K8sConfig{APIServer: cfg.Observer.APIServer, TokenFile: cfg.Observer.TokenFile, CAFile: cfg.Observer.CAFile, Insecure: cfg.Observer.Insecure}
		if k8s.APIServer == "" {
			if k8s, err = observe.InClusterConfig(); err != nil {
				return fmt.Errorf("observer enabled but no cluster config: %w", err)
			}
		}
		obs = &observe.Observer{
			Store: st, Cfg: k8s, Zones: cfg.Observer.Zones,
			AutoRegister: cfg.Observer.AutoRegister, Interval: cfg.Observer.Interval, Log: log,
		}
		go obs.Run(ctx)
	}

	srv := &api.Server{Store: st, Alloc: alloc, Ports: ports, APIKey: os.Getenv(cfg.API.KeyEnv), Log: log}
	if obs != nil {
		srv.Observer = obs
	}
	if sup != nil {
		srv.ProcessCtl = sup
	}
	go srv.RunGaugeRefresher(ctx, 15*time.Second)

	if cfg.DNS.Enabled {
		d := dnsserver.New(cfg.DNS.Zones, cfg.DNS.Listen, log)
		go func() {
			if err := d.Run(ctx); err != nil {
				log.Error("dns", "err", err)
			}
		}()
	}

	if cfg.Proxy.Enabled {
		var ca *proxy.CA
		if cfg.Proxy.CACert != "" {
			ca, err = proxy.LoadCA(cfg.Proxy.CACert, cfg.Proxy.CAKey)
		} else {
			dir := cfg.Proxy.CADir
			if dir == "" {
				dir = filepath.Join(filepath.Dir(cfg.DB), "ca")
			}
			ca, err = proxy.EnsureCA(dir)
		}
		if err != nil {
			return fmt.Errorf("proxy CA: %w", err)
		}
		var starter proxy.Starter
		if sup != nil {
			starter = sup
		}
		p := proxy.New(st, ca, starter, proxy.Options{
			HTTPAddr: cfg.Proxy.HTTP, TLSAddr: cfg.Proxy.TLS,
			ExtraTLSPorts: cfg.Proxy.ExtraTLSPorts, Log: log,
		})
		go func() {
			if err := p.Run(ctx); err != nil && ctx.Err() == nil {
				log.Error("proxy exited", "err", err)
				cancel()
			}
		}()
	}

	httpSrv := &http.Server{Addr: cfg.API.Listen, Handler: srv.Handler()}
	go func() {
		<-ctx.Done()
		sc, c2 := context.WithTimeout(context.Background(), 5*time.Second)
		defer c2()
		httpSrv.Shutdown(sc)
	}()
	log.Info("gerry serve", "version", version, "api", cfg.API.Listen, "db", cfg.DB,
		"proxy", cfg.Proxy.Enabled, "dns", cfg.DNS.Enabled, "observer", cfg.Observer.Enabled, "supervise", cfg.Supervise)
	if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// --- client commands ---

func cmdClaim(args []string) error {
	fs := flag.NewFlagSet("claim", flag.ExitOnError)
	zone := fs.String("zone", "", "zone")
	label := fs.String("label", "", "label")
	owner := fs.String("owner", "", "owner_ref")
	kind := fs.String("kind", "tenant", "kind")
	hold := fs.Bool("hold", false, "hold instead of active claim")
	pool := fs.String("port-pool", "", "also claim a sticky port from this pool")
	fs.Parse(args)
	var out map[string]any
	err := apiClient().Do(context.Background(), "POST", "/v1/claims", map[string]any{
		"zone": *zone, "label": *label, "owner_ref": *owner, "kind": *kind,
		"hold": *hold, "port_pool": *pool,
	}, &out)
	if err != nil {
		return err
	}
	printJSON(out)
	return nil
}

func cmdPort(args []string) error {
	fs := flag.NewFlagSet("port", flag.ExitOnError)
	owner := fs.String("owner", "", "owner_ref")
	pool := fs.String("pool", "dev", "pool")
	quiet := fs.Bool("q", false, "print only the port number (for scripts)")
	fs.Parse(args)
	var out map[string]any
	if err := apiClient().Do(context.Background(), "POST", "/v1/ports", map[string]any{"pool": *pool, "owner_ref": *owner}, &out); err != nil {
		return err
	}
	if *quiet {
		fmt.Println(int(out["value"].(float64)))
		return nil
	}
	printJSON(out)
	return nil
}

func cmdZone(args []string) error {
	if len(args) < 1 || args[0] != "add" {
		return fmt.Errorf("usage: gerry zone add --name Z [--profile dev|prod]")
	}
	fs := flag.NewFlagSet("zone add", flag.ExitOnError)
	name := fs.String("name", "", "zone name")
	profile := fs.String("profile", "dev", "profile")
	fs.Parse(args[1:])
	var out map[string]any
	if err := apiClient().Do(context.Background(), "POST", "/v1/zones", map[string]any{"name": *name, "profile": *profile}, &out); err != nil {
		return err
	}
	printJSON(out)
	return nil
}

func cmdAvail(args []string) error {
	fs := flag.NewFlagSet("avail", flag.ExitOnError)
	zone := fs.String("zone", "", "zone")
	label := fs.String("label", "", "label")
	fs.Parse(args)
	var out map[string]any
	if err := apiClient().Do(context.Background(), "GET", "/v1/zones/"+*zone+"/availability?label="+*label, nil, &out); err != nil {
		return err
	}
	printJSON(out)
	return nil
}

func cmdLs(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	zone := fs.String("zone", "", "zone filter")
	owner := fs.String("owner", "", "owner filter")
	fs.Parse(args)
	q := "?"
	if *zone != "" {
		q += "zone=" + *zone + "&"
	}
	if *owner != "" {
		q += "owner_ref=" + *owner
	}
	var out struct {
		Allocations []core.Allocation `json:"allocations"`
	}
	if err := apiClient().Do(context.Background(), "GET", "/v1/allocations"+q, nil, &out); err != nil {
		return err
	}
	for _, a := range out.Allocations {
		fmt.Printf("%-6d %-30s %-9s %-9s %-9s %s\n", a.ID, a.FQDN, a.Kind, a.State, a.Source, a.OwnerRef)
	}
	return nil
}

func cmdRelease(args []string) error {
	fs := flag.NewFlagSet("release", flag.ExitOnError)
	id := fs.Int64("id", 0, "allocation id")
	fs.Parse(args)
	return apiClient().Do(context.Background(), "DELETE", "/v1/allocations/"+strconv.FormatInt(*id, 10), nil, nil)
}

func cmdRename(args []string) error {
	fs := flag.NewFlagSet("rename", flag.ExitOnError)
	id := fs.Int64("id", 0, "allocation id")
	label := fs.String("label", "", "new label")
	fs.Parse(args)
	var out map[string]any
	if err := apiClient().Do(context.Background(), "POST", fmt.Sprintf("/v1/allocations/%d/rename", *id), map[string]any{"label": *label}, &out); err != nil {
		return err
	}
	printJSON(out)
	return nil
}

func cmdConflicts(args []string) error {
	var out map[string]any
	if err := apiClient().Do(context.Background(), "GET", "/v1/conflicts", nil, &out); err != nil {
		return err
	}
	printJSON(out)
	return nil
}

// --- manifest apply/release ---

func cmdUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	file := fs.String("f", "gerrymander.yaml", "manifest file")
	fs.Parse(args)
	m, err := manifest.Load(*file)
	if err != nil {
		return err
	}
	c := apiClient()
	ctx := context.Background()
	// A project manifest introducing a fresh dev zone shouldn't require a
	// daemon config edit — ensure it exists (idempotent).
	if err := c.Do(ctx, "POST", "/v1/zones", map[string]any{"name": m.Zone, "profile": "dev"}, nil); err != nil {
		return fmt.Errorf("ensure zone %s: %w", m.Zone, err)
	}
	resolvePort := func(pool, ownerRef string) (int, error) {
		var out core.PortAllocation
		if err := c.Do(ctx, "POST", "/v1/ports", map[string]any{"pool": pool, "owner_ref": ownerRef}, &out); err != nil {
			return 0, err
		}
		return out.Value, nil
	}
	claims, err := m.Claims(resolvePort)
	if err != nil {
		return err
	}
	for _, cl := range claims {
		var out struct {
			Allocation core.Allocation `json:"allocation"`
		}
		err := c.Do(ctx, "POST", "/v1/claims", map[string]any{
			"zone": cl.Zone, "label": cl.Label, "kind": "platform", "source": "manifest",
			"project": cl.Project, "owner_ref": cl.OwnerRef, "spec": cl.Spec,
		}, &out)
		if err == nil {
			fmt.Printf("claimed  %s (id %d)\n", out.Allocation.FQDN, out.Allocation.ID)
			continue
		}
		apiErr, ok := err.(*client.APIError)
		if !ok || (apiErr.Reason != "taken" && apiErr.Reason != "reserved") {
			return fmt.Errorf("claim %s.%s: %w", cl.Label, cl.Zone, err)
		}
		// Already allocated: update spec in place when we own it.
		existing, ferr := findAllocation(ctx, c, cl.Zone, cl.Label)
		if ferr != nil {
			return fmt.Errorf("claim %s.%s: taken and not listable: %v", cl.Label, cl.Zone, ferr)
		}
		if existing.OwnerRef != cl.OwnerRef && existing.Source != core.SourceManifest {
			return fmt.Errorf("claim %s.%s: owned by %s (%s) — refusing to overwrite", cl.Label, cl.Zone, existing.OwnerRef, existing.Kind)
		}
		if err := c.Do(ctx, "PATCH", fmt.Sprintf("/v1/allocations/%d", existing.ID), map[string]any{"spec": cl.Spec}, nil); err != nil {
			return err
		}
		fmt.Printf("updated  %s (id %d)\n", existing.FQDN, existing.ID)
	}
	return nil
}

func cmdDown(args []string) error {
	fs := flag.NewFlagSet("down", flag.ExitOnError)
	file := fs.String("f", "gerrymander.yaml", "manifest file")
	fs.Parse(args)
	m, err := manifest.Load(*file)
	if err != nil {
		return err
	}
	c := apiClient()
	ctx := context.Background()
	claims, err := m.Claims(func(pool, owner string) (int, error) { return 1, nil }) // ports not needed for release
	if err != nil {
		return err
	}
	for _, cl := range claims {
		existing, ferr := findAllocation(ctx, c, cl.Zone, cl.Label)
		if ferr != nil {
			continue
		}
		if existing.OwnerRef != cl.OwnerRef {
			fmt.Printf("skip     %s (owned by %s)\n", existing.FQDN, existing.OwnerRef)
			continue
		}
		if err := c.Do(ctx, "DELETE", fmt.Sprintf("/v1/allocations/%d", existing.ID), nil, nil); err != nil {
			return err
		}
		fmt.Printf("released %s\n", existing.FQDN)
	}
	return nil
}

func findAllocation(ctx context.Context, c *client.Client, zone, label string) (core.Allocation, error) {
	var out struct {
		Allocations []core.Allocation `json:"allocations"`
	}
	if err := c.Do(ctx, "GET", "/v1/allocations?zone="+zone, nil, &out); err != nil {
		return core.Allocation{}, err
	}
	for _, a := range out.Allocations {
		if a.Label == label {
			return a, nil
		}
	}
	return core.Allocation{}, fmt.Errorf("allocation %s in %s not found", label, zone)
}

// --- mcp ---

func cmdMCP(args []string) error {
	s := &mcp.Server{API: apiClient(), In: os.Stdin, Out: os.Stdout}
	return s.Run(context.Background())
}

// --- ca export ---

func cmdCAExport(args []string) error {
	fs := flag.NewFlagSet("ca-export", flag.ExitOnError)
	dir := fs.String("dir", "", "CA directory (default <db dir>/ca)")
	fs.Parse(args)
	d := *dir
	if d == "" {
		home, _ := os.UserHomeDir()
		d = filepath.Join(home, ".gerrymander", "ca")
	}
	ca, err := proxy.EnsureCA(d)
	if err != nil {
		return err
	}
	os.Stdout.Write(ca.RootPEM())
	return nil
}

// silence unused-import lint for net (used indirectly on some builds)
var _ = net.JoinHostPort
