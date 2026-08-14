// gerry is the gerrymander CLI: server, client commands, manifest apply, MCP.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Nano112/gerrymander/internal/actuate"
	"github.com/Nano112/gerrymander/internal/api"
	"github.com/Nano112/gerrymander/internal/client"
	"github.com/Nano112/gerrymander/internal/config"
	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/dnsserver"
	"github.com/Nano112/gerrymander/internal/dockerlabels"
	"github.com/Nano112/gerrymander/internal/dockerrelay"
	"github.com/Nano112/gerrymander/internal/k8slite"
	"github.com/Nano112/gerrymander/internal/manifest"
	"github.com/Nano112/gerrymander/internal/mcp"
	"github.com/Nano112/gerrymander/internal/observe"
	"github.com/Nano112/gerrymander/internal/proxy"
	"github.com/Nano112/gerrymander/internal/service"
	"github.com/Nano112/gerrymander/internal/store"
	"github.com/Nano112/gerrymander/internal/supervise"
)

var version = "dev" // overridden at build time via -ldflags "-X main.version=..."

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
	case "token":
		err = cmdToken(args)
	case "run":
		err = cmdRun(args)
	case "dev":
		err = cmdDev(args)
	case "init":
		err = cmdInit(args)
	case "status", "doctor":
		err = cmdStatus(args)
	case "service":
		err = cmdService(args)
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
	case "trust":
		err = cmdTrust(args)
	case "setup":
		err = cmdSetup(args)
	case "uninstall":
		err = cmdUninstall(args)
	case "completion":
		err = cmdCompletion(args)
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
  trust [--print]              install the daemon's CA into the system trust store
  setup [--print]              fresh machine: DNS for dev zones + trust, reversibly
  uninstall [--yes] [--purge]  full cleanse; removes ONLY gerry-marked files/certs
                               (dry-run by default; can never break your DNS)

client (env: GERRY_API, GERRY_API_KEY):
  claim  --zone Z --label L [--owner O] [--kind tenant|platform] [--hold]
  port   --owner O [--pool dev] [-q]
  zone   add --name Z [--profile dev|prod]
  run    --owner O [--pool dev] -- CMD [args…]
         claims O's sticky port, then runs CMD with PORT set and any
         literal {PORT} in the args replaced (e.g. vite --port {PORT})
  dev    [service] [-f gerrymander.yaml]
         set-and-forget for any runtime: applies the manifest, grants the
         service's sticky port, and runs its manifest-declared dev: command
  avail  --zone Z --label L
  ls     [--zone Z] [--owner O]
  release --id N
  rename --id N --label NEW       atomic; keeps id/owner/routes/history
  conflicts
  init   [--name P] [--zone Z]  scaffold a gerrymander.yaml here
  status                        doctor: daemon/DNS/proxy/trust checks + fixes
  service install|status|…      run the daemon as a launchd agent (host mode)
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

	if cfg.Actuator.Enabled {
		k8s := k8slite.Config{APIServer: cfg.Observer.APIServer, TokenFile: cfg.Observer.TokenFile, CAFile: cfg.Observer.CAFile, Insecure: cfg.Observer.Insecure}
		if k8s.APIServer == "" {
			if k8s, err = k8slite.InCluster(); err != nil {
				return fmt.Errorf("actuator enabled but no cluster config: %w", err)
			}
		}
		kc, err := k8slite.New(k8s)
		if err != nil {
			return fmt.Errorf("actuator client: %w", err)
		}
		act := &actuate.Actuator{
			Store: st, Client: kc, Zones: cfg.Actuator.Zones,
			EntryPoints: cfg.Actuator.EntryPoints, Interval: cfg.Actuator.Interval, Log: log,
		}
		go act.Run(ctx)
	}

	apiKey := os.Getenv(cfg.API.KeyEnv)
	if apiKey == "" && !isLoopbackListen(cfg.API.Listen) && !cfg.API.AllowUnauthenticated {
		return fmt.Errorf("api.listen %s is reachable off-host but %s is empty — refusing to serve an open registry (set the key, bind to 127.0.0.1, or set api.allow_unauthenticated: true)", cfg.API.Listen, cfg.API.KeyEnv)
	}
	srv := &api.Server{Store: st, Alloc: alloc, Ports: ports, APIKey: apiKey, Log: log,
		HideMetrics: cfg.API.MetricsListen != ""}
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
		srv.CAPEM = ca.RootPEM()
		srv.OnMutation = p.RequestRebuild
		// Docker relay backends need the docker CLI on this host (host
		// mode); enabled opportunistically.
		if _, err := exec.LookPath("docker"); err == nil {
			p.SetDockerResolver(dockerrelay.NewManager(ports))
			// Compose-label auto-claim: containers labeled
			// gerrymander.hostname get a route for the container's life.
			if e := cfg.DockerLabels.Enabled; e == nil || *e {
				dw := &dockerlabels.Watcher{
					Store: st, Alloc: alloc, Interval: cfg.DockerLabels.Interval,
					Log: log, OnMutation: p.RequestRebuild,
				}
				go dw.Run(ctx)
			}
		}
		go func() {
			// A busy port must degrade the proxy, not kill the daemon:
			// the registry API stays useful (and the doctor explains what
			// holds the port).
			if err := p.Run(ctx); err != nil && ctx.Err() == nil {
				log.Error("proxy disabled", "err", err, "holder", portHolder(cfg.Proxy.TLS))
			}
		}()
	}

	if cfg.API.MetricsListen != "" {
		mm := http.NewServeMux()
		mm.Handle("GET /metrics", promhttp.Handler())
		go func() {
			log.Info("metrics listener", "addr", cfg.API.MetricsListen)
			if err := http.ListenAndServe(cfg.API.MetricsListen, mm); err != nil {
				log.Error("metrics listener", "err", err)
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
		if strings.Contains(err.Error(), "address already in use") {
			if otherGerryAt(cfg.API.Listen) {
				return fmt.Errorf("another gerry daemon already serves %s (container mode running? stop it first: docker compose down in deploy/dev, or gerry service uninstall)", cfg.API.Listen)
			}
			return fmt.Errorf("%s is busy (%s) — change api.listen in the config", cfg.API.Listen, portHolder(cfg.API.Listen))
		}
		return err
	}
	return nil
}

// portHolder best-effort identifies what listens on an addr ("host:port")
// so bind-conflict errors name the culprit instead of shrugging.
// isLoopbackListen reports whether a listen address can only be reached from
// this host. ":4780" and "0.0.0.0:…" are NOT loopback.
func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func portHolder(addr string) string {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "unknown"
	}
	out, err := exec.Command("lsof", "-nP", "-iTCP:"+addr[i+1:], "-sTCP:LISTEN", "-Fc").Output()
	if err != nil {
		return "unknown holder"
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "c") {
			return "held by " + strings.TrimPrefix(l, "c")
		}
	}
	return "unknown holder"
}

// otherGerryAt reports whether a gerry daemon answers on the API address.
func otherGerryAt(listen string) bool {
	host := listen
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + host + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b := make([]byte, 8)
	n, _ := resp.Body.Read(b)
	return resp.StatusCode == 200 && strings.HasPrefix(string(b[:n]), "ok")
}

// --- client commands ---

func cmdClaim(args []string) error {
	fs := flag.NewFlagSet("claim", flag.ExitOnError)
	zone := fs.String("zone", "", "zone")
	label := fs.String("label", "", "label")
	owner := fs.String("owner", "", "owner_ref")
	kind := fs.String("kind", "", "kind (default: platform in dev zones, tenant in prod)")
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
	if len(args) < 1 {
		return fmt.Errorf("usage: gerry zone add|rm --name Z")
	}
	switch args[0] {
	case "add":
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
	case "rm":
		fs := flag.NewFlagSet("zone rm", flag.ExitOnError)
		name := fs.String("name", "", "zone name")
		fs.Parse(args[1:])
		if err := apiClient().Do(context.Background(), "DELETE", "/v1/zones/"+*name, nil, nil); err != nil {
			return err
		}
		fmt.Println("removed zone", *name)
		return nil
	default:
		return fmt.Errorf("usage: gerry zone add|rm --name Z")
	}
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

// cmdRename supports both spellings:
//
//	gerry rename gv.olsyn.test newname     (positional, human)
//	gerry rename --id 12 --label newname   (flags, scripts)
func cmdRename(args []string) error {
	fs := flag.NewFlagSet("rename", flag.ExitOnError)
	id := fs.Int64("id", 0, "allocation id")
	label := fs.String("label", "", "new label")
	fs.Parse(args)
	ctx := context.Background()
	c := apiClient()

	targetID, newLabel := *id, *label
	if targetID == 0 && fs.NArg() == 2 {
		fqdn := strings.ToLower(fs.Arg(0))
		newLabel = fs.Arg(1)
		var out struct {
			Allocations []core.Allocation `json:"allocations"`
		}
		if err := c.Do(ctx, "GET", "/v1/allocations", nil, &out); err != nil {
			return err
		}
		for _, a := range out.Allocations {
			if a.FQDN == fqdn {
				targetID = a.ID
				break
			}
		}
		if targetID == 0 {
			return fmt.Errorf("no allocation for %q (see: gerry ls)", fqdn)
		}
	}
	if targetID == 0 || newLabel == "" {
		return fmt.Errorf("usage: gerry rename <fqdn> <new-label>   (or --id N --label X)")
	}
	var out map[string]any
	if err := c.Do(ctx, "POST", fmt.Sprintf("/v1/allocations/%d/rename", targetID), map[string]any{"label": newLabel}, &out); err != nil {
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

// --- dev ---

// cmdDev is `bun run dev` for everything that isn't vite. One service runs
// in the foreground; with no argument every dev:-declared service runs
// concurrently, procfile-style, with prefixed output and group shutdown.
func cmdDev(args []string) error {
	fs := flag.NewFlagSet("dev", flag.ExitOnError)
	file := fs.String("f", "gerrymander.yaml", "manifest file")
	fs.Parse(args)
	m, err := manifest.Load(*file)
	if err != nil {
		return err
	}

	var withDev []string
	for n, s := range m.Services {
		if s.Dev != "" {
			withDev = append(withDev, n)
		}
	}
	sort.Strings(withDev)
	if len(withDev) == 0 {
		return fmt.Errorf("no service in %s declares a dev: command", *file)
	}

	var run []string
	if fs.NArg() > 0 {
		name := fs.Arg(0)
		if _, ok := m.Services[name]; !ok {
			return fmt.Errorf("service %q not in %s", name, *file)
		}
		if m.Services[name].Dev == "" {
			return fmt.Errorf("service %q has no dev: command in %s", name, *file)
		}
		run = []string{name}
	} else {
		run = withDev // all of them
	}

	// Apply the whole manifest once so hostnames route before anything
	// starts, then launch each service on its sticky port.
	c := apiClient()
	ctx := context.Background()
	yamlBytes, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var applied struct {
		Services map[string]struct {
			Hostnames []string `json:"hostnames"`
			Port      int      `json:"port"`
		} `json:"services"`
		Pruned []string `json:"pruned"`
	}
	if err := c.Do(ctx, "POST", "/v1/manifest/apply", map[string]any{"yaml": string(yamlBytes)}, &applied); err != nil {
		return fmt.Errorf("apply %s: %w", *file, err)
	}
	for _, p := range applied.Pruned {
		fmt.Fprintf(os.Stderr, "gerry: released %s (left the manifest)\n", p)
	}

	portFor := func(name string) (int, error) {
		if p := applied.Services[name].Port; p != 0 {
			return p, nil
		}
		var pa core.PortAllocation
		err := c.Do(ctx, "POST", "/v1/ports", map[string]any{"pool": "dev", "owner_ref": m.Project + "/" + name}, &pa)
		return pa.Value, err
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	// Single service: plain foreground exec, stdio attached.
	if len(run) == 1 {
		name := run[0]
		port, err := portFor(name)
		if err != nil {
			return err
		}
		if hosts := applied.Services[name].Hostnames; len(hosts) > 0 {
			fmt.Fprintf(os.Stderr, "gerry: https://%s → :%d\n", hosts[0], port)
		}
		cmd := exec.Command(shell, "-c", strings.ReplaceAll(m.Services[name].Dev, "{PORT}", strconv.Itoa(port)))
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Env = append(os.Environ(), "PORT="+strconv.Itoa(port), "GERRY_PORT="+strconv.Itoa(port))
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				os.Exit(ee.ExitCode())
			}
			return err
		}
		return nil
	}

	// Procfile mode: run everything, prefix output, die together.
	colors := []string{"\x1b[36m", "\x1b[35m", "\x1b[33m", "\x1b[32m", "\x1b[34m"}
	if os.Getenv("NO_COLOR") != "" || !isTTY(os.Stdout) {
		colors = []string{""}
	}
	reset := "\x1b[0m"
	if colors[0] == "" {
		reset = ""
	}
	width := 0
	for _, n := range run {
		if len(n) > width {
			width = len(n)
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	go func() { <-sigc; cancel() }()

	var wg sync.WaitGroup
	var exitOnce sync.Once
	exitCode := 0
	for i, name := range run {
		port, err := portFor(name)
		if err != nil {
			return err
		}
		color := colors[i%len(colors)]
		prefix := fmt.Sprintf("%s%-*s |%s ", color, width, name, reset)
		if hosts := applied.Services[name].Hostnames; len(hosts) > 0 {
			fmt.Fprintf(os.Stderr, "%sgerry: https://%s → :%d\n", prefix, hosts[0], port)
		}
		cmd := exec.CommandContext(runCtx, shell, "-c", strings.ReplaceAll(m.Services[name].Dev, "{PORT}", strconv.Itoa(port)))
		cmd.Env = append(os.Environ(), "PORT="+strconv.Itoa(port), "GERRY_PORT="+strconv.Itoa(port))
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
		stdout, _ := cmd.StdoutPipe()
		stderr, _ := cmd.StderrPipe()
		if err := cmd.Start(); err != nil {
			cancel()
			return fmt.Errorf("start %s: %w", name, err)
		}
		prefixPipe := func(r io.Reader) {
			defer wg.Done()
			sc := bufio.NewScanner(r)
			sc.Buffer(make([]byte, 64*1024), 1024*1024)
			for sc.Scan() {
				fmt.Printf("%s%s\n", prefix, sc.Text())
			}
		}
		wg.Add(2)
		go prefixPipe(stdout)
		go prefixPipe(stderr)
		wg.Add(1)
		go func(prefix string) {
			defer wg.Done()
			err := cmd.Wait()
			if runCtx.Err() == nil { // a service died on its own: stop the set
				fmt.Fprintf(os.Stderr, "%sexited: %v — stopping the rest\n", prefix, err)
				exitOnce.Do(func() { exitCode = 1 })
				cancel()
			}
		}(prefix)
	}
	wg.Wait()
	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}

// --- init ---

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	name := fs.String("name", "", "project name (default: directory name)")
	zone := fs.String("zone", "", "zone (default: <name>.test)")
	fs.Parse(args)
	if _, err := os.Stat("gerrymander.yaml"); err == nil {
		return fmt.Errorf("gerrymander.yaml already exists")
	}
	n := *name
	if n == "" {
		wd, _ := os.Getwd()
		n = strings.ToLower(filepath.Base(wd))
	}
	z := *zone
	if z == "" {
		z = n + ".test"
	}
	content := fmt.Sprintf(`project: %s
zone: %s
services:
  # With @gerrymander/vite in vite.config, "bun run dev" claims this
  # hostname and its sticky port automatically. For other tools:
  #   gerry run --owner %s/frontend -- CMD --port '{PORT}'
  frontend:
    hostnames: [%s, "*.%s"]
    port_pool: dev
  # api:
  #   hostnames: [api.%s]
  #   port_pool: dev
`, n, z, n, z, z, z)
	if err := os.WriteFile("gerrymander.yaml", []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote gerrymander.yaml (project %s, zone %s)\nnext: gerry up   # or just start vite with @gerrymander/vite\n", n, z)
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

// --- run ---

// cmdRun bridges the registry to any dev tool: fetch the owner's sticky
// port, expose it as $PORT and as a literal {PORT} substitution, exec the
// command with stdio attached. Ctrl-C reaches the child (same process
// group); gerry adds no supervision here — it's a port courier.
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	owner := fs.String("owner", "", "owner_ref for the sticky port")
	pool := fs.String("pool", "dev", "port pool")
	fs.Parse(args)
	rest := fs.Args()
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if *owner == "" || len(rest) == 0 {
		return fmt.Errorf("usage: gerry run --owner O [--pool dev] -- CMD [args…]")
	}
	var pa core.PortAllocation
	if err := apiClient().Do(context.Background(), "POST", "/v1/ports", map[string]any{"pool": *pool, "owner_ref": *owner}, &pa); err != nil {
		return err
	}
	port := strconv.Itoa(pa.Value)
	argv := make([]string, len(rest))
	for i, a := range rest {
		argv[i] = strings.ReplaceAll(a, "{PORT}", port)
	}
	fmt.Fprintf(os.Stderr, "gerry: %s → port %s\n", *owner, port)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "PORT="+port, "GERRY_PORT="+port)
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}

// --- mcp ---

func cmdMCP(args []string) error {
	s := &mcp.Server{API: apiClient(), In: os.Stdin, Out: os.Stdout}
	return s.Run(context.Background())
}

// --- ca export ---

// proxyEnsureCA loads-or-creates the host CA dir and returns the root PEM
// (shared by ca-export and trust's offline fallback).
func proxyEnsureCA(dir string) ([]byte, error) {
	ca, err := proxy.EnsureCA(dir)
	if err != nil {
		return nil, err
	}
	return ca.RootPEM(), nil
}

func cmdCAExport(args []string) error {
	fs := flag.NewFlagSet("ca-export", flag.ExitOnError)
	dir := fs.String("dir", "", "CA directory (default <db dir>/ca)")
	fs.Parse(args)
	d := *dir
	if d == "" {
		home, _ := os.UserHomeDir()
		d = filepath.Join(home, ".gerrymander", "ca")
	}
	pem, err := proxyEnsureCA(d)
	if err != nil {
		return err
	}
	os.Stdout.Write(pem)
	return nil
}

// silence unused-import lint for net (used indirectly on some builds)
var _ = net.JoinHostPort
