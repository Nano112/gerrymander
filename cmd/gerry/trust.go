package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// gerry trust — make browsers trust the daemon's CA in one command. Fetches
// the exact certificate the proxy mints with (GET /v1/ca) and installs it
// into the system trust store, sudo-prompting inline.
func cmdTrust(args []string) error {
	fs := flag.NewFlagSet("trust", flag.ExitOnError)
	printOnly := fs.Bool("print", false, "print the install commands instead of running them")
	fs.Parse(args)

	// Fetch the CA from the running daemon (fall back to the local host-mode
	// CA dir so trust works before first `gerry serve`).
	pem, src, err := fetchCA()
	if err != nil {
		return err
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	os.MkdirAll(filepath.Join(cacheDir, "gerrymander"), 0o755)
	tmp := filepath.Join(cacheDir, "gerrymander", "ca.pem")
	if err := os.WriteFile(tmp, pem, 0o644); err != nil {
		return err
	}
	fmt.Printf("CA from %s → %s\n", src, tmp)

	var cmds [][]string
	switch runtime.GOOS {
	case "darwin":
		cmds = [][]string{{"sudo", "security", "add-trusted-cert", "-d", "-r", "trustRoot",
			"-k", "/Library/Keychains/System.keychain", tmp}}
	case "linux":
		if _, err := exec.LookPath("update-ca-certificates"); err == nil {
			cmds = [][]string{
				{"sudo", "cp", tmp, "/usr/local/share/ca-certificates/gerrymander.crt"},
				{"sudo", "update-ca-certificates"},
			}
		} else if _, err := exec.LookPath("trust"); err == nil {
			cmds = [][]string{{"sudo", "trust", "anchor", tmp}}
		} else {
			return fmt.Errorf("no known trust tool (update-ca-certificates / trust); install the CA at %s manually", tmp)
		}
	case "windows":
		// BETA: certutil ships with Windows; -user avoids needing admin.
		cmds = [][]string{{"certutil", "-user", "-addstore", "Root", tmp}}
	default:
		return fmt.Errorf("gerry trust supports macOS, Linux and Windows (beta); install %s into your trust store manually", tmp)
	}

	for _, c := range cmds {
		fmt.Println(" ", joinArgs(c))
		if *printOnly {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", c[0], err)
		}
	}
	if !*printOnly {
		fmt.Println("trusted — browsers accept the daemon's certificates now (restart the browser if one is open)")
	}
	return nil
}

func fetchCA() ([]byte, string, error) {
	base := os.Getenv("GERRY_API")
	if base == "" {
		base = "http://127.0.0.1:4780"
	}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", base+"/v1/ca", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			pem, err := io.ReadAll(resp.Body)
			return pem, base, err
		}
	}
	// Daemon not running (or proxyless): use/create the host-mode CA.
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".gerrymander", "ca")
	ca, err := proxyEnsureCA(dir)
	if err != nil {
		return nil, "", fmt.Errorf("daemon unreachable at %s and no local CA: %w", base, err)
	}
	return ca, "local " + dir, nil
}

func joinArgs(c []string) string {
	out := ""
	for i, a := range c {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
