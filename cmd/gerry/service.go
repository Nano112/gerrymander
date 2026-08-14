package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// gerry service — run the daemon as a user service (host mode): launchd on
// macOS, a systemd user unit on Linux. Host mode is what unlocks supervised
// backends: the proxy and your dev processes share a machine, so gerry can
// boot them on first request and sleep them when idle.

const launchdLabel = "com.gerrymander.daemon"
const systemdUnit = "gerrymander.service"

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
	if runtime.GOOS == "linux" {
		return cmdServiceLinux(args)
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("gerry service supports macOS (launchd) and Linux (systemd --user); on %s run `gerry serve` under your init system", runtime.GOOS)
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

// --- linux (systemd --user) ---

func cmdServiceLinux(args []string) error {
	home, _ := os.UserHomeDir()
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	unitPath := filepath.Join(unitDir, systemdUnit)

	switch args[0] {
	case "install":
		fs := flag.NewFlagSet("service install", flag.ExitOnError)
		config := fs.String("config", hostConfigPath(), "daemon config path")
		fs.Parse(args[1:])
		self, err := os.Executable()
		if err != nil {
			return err
		}
		self, _ = filepath.EvalSymlinks(self)
		writeStarterConfig(*config, home)
		os.MkdirAll(unitDir, 0o755)
		unit := `[Unit]
Description=gerrymander hostname and port control plane
After=network.target

[Service]
ExecStart=` + self + ` serve --config ` + *config + `
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`
		if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
			return err
		}
		run := func(a ...string) { exec.Command("systemctl", append([]string{"--user"}, a...)...).Run() }
		run("daemon-reload")
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", systemdUnit).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl enable: %v: %s", err, out)
		}
		fmt.Println("installed + started:", systemdUnit)
		fmt.Println("logs: journalctl --user -u", systemdUnit, "-f")
		fmt.Println("NOTE: binding ports 80/443 as a user needs:")
		fmt.Println("  sudo setcap 'cap_net_bind_service=+ep'", self)
		fmt.Println("survive logout: loginctl enable-linger", os.Getenv("USER"))
		return nil
	case "uninstall":
		exec.Command("systemctl", "--user", "disable", "--now", systemdUnit).Run()
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		exec.Command("systemctl", "--user", "daemon-reload").Run()
		fmt.Println("uninstalled", systemdUnit)
		return nil
	case "status":
		out, _ := exec.Command("systemctl", "--user", "--no-pager", "status", systemdUnit).CombinedOutput()
		os.Stdout.Write(out)
		return nil
	case "restart":
		if out, err := exec.Command("systemctl", "--user", "restart", systemdUnit).CombinedOutput(); err != nil {
			return fmt.Errorf("restart: %v: %s", err, out)
		}
		fmt.Println("restarted", systemdUnit)
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

	writeStarterConfig(*config, home)

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

// writeStarterConfig creates a host config if absent. Ports are the real
// ones — host mode replaces any container daemon on this machine.
func writeStarterConfig(config, home string) {
	if _, err := os.Stat(config); err == nil {
		return
	}
	os.MkdirAll(filepath.Dir(config), 0o755)
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
  enabled: false # enable if nothing wildcards your dev TLD to loopback
supervise: true
ports:
  ensure_default_pool: true
`
	if err := os.WriteFile(config, []byte(starter), 0o644); err == nil {
		fmt.Println("wrote starter config:", config)
	}
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
