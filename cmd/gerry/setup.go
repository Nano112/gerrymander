package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
)

// Ownership marker: gerry only ever deletes files that begin with this
// line. Everything else on the machine — Herd's resolvers, dnsmasq configs,
// somebody's hand-written /etc/resolver/test — is off-limits forever.
const ownershipMarker = "# gerrymander-managed"

// --- gerry setup: fresh-machine DNS + trust, reversibly ---

func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	printOnly := fs.Bool("print", false, "show what would be done without doing it")
	fs.Parse(args)

	fmt.Println("gerry setup — DNS + TLS trust for your dev zones")
	fmt.Println()

	// Which TLDs matter? The daemon's dev zones, else .test.
	tlds := map[string]bool{}
	c := apiClient()
	var zones struct {
		Zones []core.Zone `json:"zones"`
	}
	if err := c.Do(context.Background(), "GET", "/v1/zones", nil, &zones); err == nil {
		for _, z := range zones.Zones {
			if z.Profile == "dev" {
				tlds[tld(z.Name)] = true
			}
		}
	}
	if len(tlds) == 0 {
		tlds["test"] = true
	}

	needDNS := []string{}
	for t := range tlds {
		probe := "gerry-setup-probe.zzz." + t
		if addrs, err := net.DefaultResolver.LookupHost(context.Background(), probe); err == nil && len(addrs) > 0 {
			owner := "an existing resolver (dnsmasq / Herd / Valet?) — leaving it untouched"
			if resolverFileOurs(t) {
				owner = "gerry's own resolver entry"
			}
			fmt.Printf("  ✓ *.%s already resolves via %s\n", t, owner)
			continue
		}
		needDNS = append(needDNS, t)
	}

	for _, t := range needDNS {
		switch runtime.GOOS {
		case "darwin":
			path := "/etc/resolver/" + t
			content := ownershipMarker + "\nnameserver 127.0.0.1\nport 5353\n"
			fmt.Printf("  wildcard *.%s → gerry's embedded DNS (127.0.0.1:5353)\n", t)
			fmt.Printf("    sudo tee %s   # marker-tagged; `gerry uninstall` removes it cleanly\n", path)
			if !*printOnly {
				cmd := exec.Command("sudo", "tee", path)
				cmd.Stdin = strings.NewReader(content)
				cmd.Stdout, cmd.Stderr = nil, os.Stderr
				if err := cmd.Run(); err != nil {
					return fmt.Errorf("write %s: %w", path, err)
				}
			}
			fmt.Println("    (enable dns in the daemon config: dns: { enabled: true, zones: [" + t + "] })")
		default:
			fmt.Printf("  *.%s does not resolve — on Linux point your resolver at gerry's DNS manually:\n", t)
			fmt.Println("    dnsmasq: address=/." + t + "/127.0.0.1  (or resolved per-link domain routing)")
			fmt.Println("    then enable dns in the daemon config (dns: { enabled: true, zones: [" + t + "] })")
		}
	}

	fmt.Println()
	fmt.Println("TLS trust:")
	trustArgs := []string{}
	if *printOnly {
		trustArgs = append(trustArgs, "--print")
	}
	return cmdTrust(trustArgs)
}

// resolverFileOurs reports whether /etc/resolver/<tld> exists AND carries
// gerry's marker.
func resolverFileOurs(t string) bool {
	b, err := os.ReadFile("/etc/resolver/" + t)
	return err == nil && strings.HasPrefix(string(b), ownershipMarker)
}

// --- gerry uninstall: the cleanse that cannot break DNS ---

// The rule that makes it safe: every removal is either (a) a file carrying
// the ownership marker, (b) a cert with gerry's own CN, (c) a container
// with gerry's label, or (d) gerry's own state directory. Anything gerry
// merely *used* (an inherited CA, someone else's resolver, dnsmasq) is
// reported and left alone.
func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	yes := fs.Bool("yes", false, "actually remove (default: print the plan)")
	purge := fs.Bool("purge", false, "also delete ~/.gerrymander (registry db, CA, config, logs)")
	fs.Parse(args)

	home, _ := os.UserHomeDir()
	type action struct {
		desc string
		run  func() error
	}
	var plan []action
	add := func(desc string, run func() error) { plan = append(plan, action{desc, run}) }
	sudo := func(a ...string) error {
		cmd := exec.Command("sudo", a...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}

	// 1. background service
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat(plistPath()); err == nil {
			add("stop + remove launchd agent "+launchdLabel, serviceUninstall)
		}
	case "linux":
		unit := filepath.Join(home, ".config", "systemd", "user", systemdUnit)
		if _, err := os.Stat(unit); err == nil {
			add("stop + remove systemd user unit "+systemdUnit, func() error { return cmdServiceLinux([]string{"uninstall"}) })
		}
	}

	// 2. resolver files — ONLY marker-tagged ones. Removing them restores
	// exactly the pre-gerry resolution path (upstream DNS answers, which
	// simply NXDOMAINs dev TLDs). Foreign files are reported, never touched.
	if runtime.GOOS == "darwin" {
		entries, _ := os.ReadDir("/etc/resolver")
		for _, e := range entries {
			name := e.Name()
			if resolverFileOurs(name) {
				p := "/etc/resolver/" + name
				add("remove gerry-managed resolver "+p, func() error { return sudo("rm", p) })
			} else if b, err := os.ReadFile("/etc/resolver/" + name); err == nil && strings.Contains(string(b), "127.0.0.1") {
				fmt.Printf("  keeping /etc/resolver/%s — not gerry's (no marker); owned by dnsmasq/Herd/you\n", name)
			}
		}
	} else if runtime.GOOS == "linux" {
		if crt := "/usr/local/share/ca-certificates/gerrymander.crt"; fileExists(crt) {
			add("remove "+crt+" + update-ca-certificates", func() error {
				if err := sudo("rm", crt); err != nil {
					return err
				}
				return sudo("update-ca-certificates")
			})
		}
	}

	// 3. trust store — gerry's OWN CA only, matched by its exact CN. An
	// inherited CA (e.g. Caddy's) was trusted before gerry and stays.
	if runtime.GOOS == "darwin" {
		if err := exec.Command("security", "find-certificate", "-c", "gerrymander local CA", "/Library/Keychains/System.keychain").Run(); err == nil {
			add(`remove CA "gerrymander local CA" from the system keychain (inherited CAs are untouched)`, func() error {
				return sudo("security", "delete-certificate", "-c", "gerrymander local CA", "/Library/Keychains/System.keychain")
			})
		}
	}

	// 4. relay containers (labeled)
	if out, err := exec.Command("docker", "ps", "-aq", "--filter", "label=app.gerrymander.relay").Output(); err == nil {
		ids := strings.Fields(string(out))
		if len(ids) > 0 {
			add(fmt.Sprintf("remove %d docker relay container(s)", len(ids)), func() error {
				return exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run()
			})
		}
	}

	// 5. state
	stateDir := filepath.Join(home, ".gerrymander")
	if *purge {
		if fileExists(stateDir) {
			add("purge "+stateDir+" (registry db, CA, config, logs)", func() error { return os.RemoveAll(stateDir) })
		}
		if cache, err := os.UserCacheDir(); err == nil && fileExists(filepath.Join(cache, "gerrymander")) {
			add("purge "+filepath.Join(cache, "gerrymander"), func() error { return os.RemoveAll(filepath.Join(cache, "gerrymander")) })
		}
	} else if fileExists(stateDir) {
		fmt.Printf("  keeping %s (registry, sticky ports, CA) — add --purge to delete\n", stateDir)
	}

	if len(plan) == 0 {
		fmt.Println("nothing to remove — gerry left no footprint on this machine")
		return nil
	}
	fmt.Println()
	fmt.Println("plan:")
	for _, a := range plan {
		fmt.Println("  -", a.desc)
	}
	if !*yes {
		fmt.Println("\ndry run — re-run with --yes to execute")
		return nil
	}
	fmt.Println()
	for _, a := range plan {
		fmt.Println("→", a.desc)
		if err := a.run(); err != nil {
			return fmt.Errorf("%s: %w", a.desc, err)
		}
	}
	if self, err := os.Executable(); err == nil {
		fmt.Println("\ndone. the binary itself remains at", self, "— delete it (or `go clean -i`) when ready")
	}
	// Give launchd/systemd a beat, then confirm DNS still works.
	time.Sleep(300 * time.Millisecond)
	if _, err := net.DefaultResolver.LookupHost(context.Background(), "example.com"); err == nil {
		fmt.Println("DNS sanity: public resolution still working ✓")
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
