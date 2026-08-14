package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The help screen, rendered with lipgloss. Styling degrades cleanly:
// termenv honors NO_COLOR and non-tty output, so pipes and scripts see
// plain text.
var (
	crayon    = lipgloss.AdaptiveColor{Light: "#6B50C7", Dark: "#8F6FE0"}
	parchment = lipgloss.AdaptiveColor{Light: "#5A5140", Dark: "#E8DCBB"}

	styTitle   = lipgloss.NewStyle().Bold(true).Foreground(crayon)
	styTagline = lipgloss.NewStyle().Faint(true)
	stySection = lipgloss.NewStyle().Bold(true).Foreground(parchment)
	styCmd     = lipgloss.NewStyle().Bold(true)
	styCont    = lipgloss.NewStyle().Faint(true)
)

type usageEntry struct {
	cmd  string
	desc []string // first line beside the command, rest indented + dim
}

type usageSection struct {
	title   string
	entries []usageEntry
}

var usageSections = []usageSection{
	{"server", []usageEntry{
		{"bootstrap [--no-setup]", []string{"one-shot first run: service + DNS + trust"}},
		{"serve --config <file>", []string{"run API (+ proxy/DNS/observer per config)"}},
		{"ca-export --dir <dir>", []string{"print the local CA root certificate PEM"}},
		{"trust [--print]", []string{"install the daemon's CA into the system trust store"}},
		{"setup [--print]", []string{"fresh machine: DNS for dev zones + trust, reversibly"}},
		{"uninstall [--yes] [--purge]", []string{"full cleanse; removes ONLY gerry-marked files/certs",
			"(dry-run by default; can never break your DNS)"}},
		{"update [--check]", []string{"self-update to the latest release (brew installs defer to brew)"}},
	}},
	{"projects", []usageEntry{
		{"init [--name P] [--zone Z] [--yes]", []string{"scaffold gerrymander.yaml; detects your dev",
			"command, offers to wire vite itself"}},
		{"dev [service] [-f file]", []string{"apply the manifest, grant sticky ports, run the",
			"declared dev: commands — set-and-forget for any runtime"}},
		{"run --owner O -- CMD [args…]", []string{"port courier: claims O's sticky port, runs CMD",
			"with PORT set and literal {PORT} substituted"}},
		{"up / down [-f file]", []string{"apply / release a project manifest"}},
	}},
	{"registry", []usageEntry{
		{"claim --zone Z --label L", []string{"claim a hostname ([--owner O] [--kind tenant|platform] [--hold])"}},
		{"avail --zone Z --label L", []string{"is a label claimable? returns why-not + suggestions"}},
		{"ls [--zone Z] [--owner O]", []string{"list allocations"}},
		{"rename <fqdn> <label>", []string{"atomic; keeps id/owner/routes/history"}},
		{"release --id N", []string{"give a name back"}},
		{"port --owner O [--pool dev]", []string{"sticky dev port (same owner, same port)"}},
		{"zone add|rm", []string{"manage zones"}},
		{"token create|ls|revoke", []string{"scoped API credentials (owner-confined or admin)"}},
		{"conflicts", []string{"what the observer flagged"}},
	}},
	{"environment", []usageEntry{
		{"status (doctor)", []string{"daemon/DNS/proxy/trust checks, each with its fix"}},
		{"tailnet", []string{"guided setup: dev hostnames across your tailscale —",
			"probes what works, walks through what doesn't"}},
		{"service install|status|…", []string{"run the daemon on login (launchd / systemd --user)"}},
		{"mcp", []string{"serve MCP over stdio (AI agents allocate instead of guessing)"}},
		{"completion bash|zsh|fish", []string{"shell completions"}},
	}},
}

func usage() {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", styTitle.Render("gerrymander"), styTagline.Render("— hostname and port control plane"))
	fmt.Fprintf(&b, "%s\n", styTagline.Render("usage: gerry <command> [flags]   (env: GERRY_API, GERRY_API_KEY, GERRY_TLD)"))
	for _, sec := range usageSections {
		fmt.Fprintf(&b, "\n%s\n", stySection.Render(sec.title))
		for _, e := range sec.entries {
			fmt.Fprintf(&b, "  %s %s\n", styCmd.Render(fmt.Sprintf("%-34s", e.cmd)), e.desc[0])
			for _, extra := range e.desc[1:] {
				fmt.Fprintf(&b, "  %34s %s\n", "", styCont.Render(extra))
			}
		}
	}
	fmt.Fprintf(&b, "\n%s\n", styTagline.Render("docs: https://nano112.github.io/gerrymander"))
	fmt.Fprint(os.Stderr, b.String())
}
