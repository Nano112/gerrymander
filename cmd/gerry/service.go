package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gerry service — run the daemon as a macOS launchd user agent (host mode).
// Host mode is what unlocks supervised backends: the proxy and your dev
// processes share a machine, so gerry can boot them on first request and
// sleep them when idle. (Linux systemd-user support: same shape, later.)

const launchdLabel = "com.gerrymander.daemon"

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

func hostConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gerrymander", "gerry.yaml")
}

func cmdService(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gerry service install|uninstall|status|restart")
	}
	switch args[0] {
	case "install":
		return serviceInstall(args[1:])
	case "uninstall":
		return serviceUninstall()
	case "status":
		return serviceStatus()
	case "restart":
		exec.Command("launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchdLabel)).Run()
		fmt.Println("kicked", launchdLabel)
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func serviceInstall(args []string) error {
	fs := flag.NewFlagSet("service install", flag.ExitOnError)
	config := fs.String("config", hostConfigPath(), "daemon config path")
	fs.Parse(args)

	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.EvalSymlinks(self)

	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".gerrymander", "log")
	os.MkdirAll(logDir, 0o755)

	// Write a starter host config when none exists. Ports are the real
	// ones — host mode replaces any container daemon on this machine.
	if _, err := os.Stat(*config); os.IsNotExist(err) {
		os.MkdirAll(filepath.Dir(*config), 0o755)
		starter := `# gerrymander host daemon — supervised backends enabled.
db: ` + filepath.Join(home, ".gerrymander", "gerry.db") + `
api:
  listen: 127.0.0.1:4780
proxy:
  enabled: true
  http: "127.0.0.1:80"
  tls: "127.0.0.1:443"
  extra_tls_ports: [5173, 5174, 5175, 5176]
  ca_dir: ` + filepath.Join(home, ".gerrymander", "ca") + `
dns:
  enabled: false # dnsmasq already wildcards .test on this machine
supervise: true
ports:
  ensure_default_pool: true
`
		if err := os.WriteFile(*config, []byte(starter), 0o644); err != nil {
			return err
		}
		fmt.Println("wrote starter config:", *config)
	}

	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + launchdLabel + `</string>
  <key>ProgramArguments</key><array>
    <string>` + self + `</string>
    <string>serve</string>
    <string>--config</string>
    <string>` + *config + `</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>` + filepath.Join(logDir, "gerry.log") + `</string>
  <key>StandardErrorPath</key><string>` + filepath.Join(logDir, "gerry.log") + `</string>
  <key>EnvironmentVariables</key><dict>
    <key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
</dict></plist>
`
	if err := os.WriteFile(plistPath(), []byte(plist), 0o644); err != nil {
		return err
	}
	uid := os.Getuid()
	exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, launchdLabel)).Run() // idempotent
	if out, err := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", uid), plistPath()).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
	}
	fmt.Println("installed + started:", launchdLabel)
	fmt.Println("logs:", filepath.Join(logDir, "gerry.log"))
	fmt.Println("NOTE: stop any container daemon holding ports 80/443/4780 first")
	return nil
}

func serviceUninstall() error {
	uid := os.Getuid()
	exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, launchdLabel)).Run()
	if err := os.Remove(plistPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Println("uninstalled", launchdLabel)
	return nil
}

func serviceStatus() error {
	out, err := exec.Command("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchdLabel)).CombinedOutput()
	if err != nil {
		fmt.Println("not loaded (install with: gerry service install)")
		return nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "state =") || strings.HasPrefix(l, "pid =") || strings.HasPrefix(l, "last exit") {
			fmt.Println(" ", l)
		}
	}
	return nil
}
