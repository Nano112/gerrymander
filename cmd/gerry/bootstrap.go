package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"
)

// cmdBootstrap is the whole first-run experience as one idempotent command:
// daemon on login, DNS for dev zones, TLS trust. Safe to re-run any time —
// each step detects existing state, and everything it writes is
// marker-tagged so `gerry uninstall` reverses it exactly.
func cmdBootstrap(args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	skipSetup := fs.Bool("no-setup", false, "install the daemon only; skip DNS + trust wiring")
	fs.Parse(args)

	fmt.Println("gerry bootstrap — daemon + DNS + trust in one pass")
	fmt.Println()

	base := os.Getenv("GERRY_API")
	if base == "" {
		base = "http://127.0.0.1:4780"
	}

	// 1. daemon on login — unless one is already serving (container mode, a
	// prior install): installing a second daemon onto a busy port would just
	// crash-loop, so coexist instead.
	if daemonUp(base) {
		fmt.Println("[1/3] a daemon already serves " + base + " — keeping it (skipping service install)")
	} else {
		switch runtime.GOOS {
		case "darwin", "linux":
			fmt.Println("[1/3] background service")
			if err := cmdService([]string{"install"}); err != nil {
				return fmt.Errorf("service install: %w", err)
			}
		default:
			fmt.Println("[1/3] background service: not automated on " + runtime.GOOS +
				" yet — run `gerry serve` manually (Task Scheduler on Windows)")
		}
	}

	// 2. wait for the daemon — setup fetches the CA from it
	fmt.Println("[2/3] waiting for the daemon…")
	up := false
	for i := 0; i < 30; i++ {
		if daemonUp(base) {
			up = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !up {
		return fmt.Errorf("daemon did not come up at %s — check `gerry status` and logs in ~/.gerrymander/log", base)
	}
	fmt.Println("      daemon up ✓")

	// 3. resolver + trust (reversible; skips anything already handled)
	if *skipSetup {
		fmt.Println("[3/3] setup skipped (--no-setup)")
	} else {
		fmt.Println("[3/3] DNS + TLS trust")
		if err := cmdSetup(nil); err != nil {
			return fmt.Errorf("setup: %w", err)
		}
	}

	// tailnet (optional): when tailscale is present, point at the docs —
	// dev hostnames can resolve from every device on the tailnet.
	if _, err := tailscaleIP(); err == nil {
		fmt.Println()
		fmt.Println("[+] tailscale detected — your dev hostnames can also resolve tailnet-wide")
		fmt.Println("    (phone, other machines): https://nano112.github.io/gerrymander/tailscale/")
		fmt.Println("    `gerry status` checks the tailnet wiring and names any missing piece.")
	}

	fmt.Println()
	fmt.Println("done. next:")
	fmt.Println("  cd your-project && gerry init && gerry dev")
	fmt.Println("  (vite projects: bun add -d gerrymander-vite and just run dev)")
	return nil
}

func daemonUp(base string) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(base + "/healthz")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}
