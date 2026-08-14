package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// viteWizard runs after gerry init when the project is vite-based: it
// offers to install gerrymander-vite and wire vite.config itself, so a
// working setup is `gerry init` + `npm run dev` with nothing typed in
// between. It edits exactly two things — a devDependency and two lines of
// vite config — prints what it did, and never touches a config it cannot
// parse confidently (falling back to printed instructions instead).
func viteWizard(assumeYes bool) {
	cfgPath := viteConfigPath()
	if cfgPath == "" {
		return // not a vite project
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}
	cfg := string(raw)
	if strings.Contains(cfg, "gerrymander-vite") {
		fmt.Println("vite: gerrymander-vite already wired ✓")
		return
	}

	pm := packageManager()
	if !assumeYes {
		if !askYesNo(fmt.Sprintf("vite detected — install gerrymander-vite (%s) and wire %s? [Y/n] ", pm, cfgPath)) {
			printViteInstructions(pm, cfgPath)
			return
		}
	}

	// 1. the devDependency, via whichever package manager owns the lockfile
	install := map[string][]string{
		"bun":  {"add", "-d", "gerrymander-vite"},
		"pnpm": {"add", "-D", "gerrymander-vite"},
		"yarn": {"add", "-D", "gerrymander-vite"},
		"npm":  {"install", "-D", "gerrymander-vite"},
	}[pm]
	fmt.Printf("  %s %s\n", pm, strings.Join(install, " "))
	cmd := exec.Command(pm, install...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("  install failed (%v) — run it yourself, then wire the config:\n", err)
		printViteInstructions(pm, cfgPath)
		return
	}

	// 2. the config: an import after the last import, gerrymander() first
	// in the plugins array (position is inert; inserting after the bracket
	// is the edit that cannot misparse nested arrays).
	edited, ok := wireViteConfig(cfg)
	if !ok {
		fmt.Printf("  %s has no plugins array this tool can edit confidently; add by hand:\n", cfgPath)
		printViteInstructions(pm, cfgPath)
		return
	}
	if err := os.WriteFile(cfgPath, []byte(edited), 0o644); err != nil {
		fmt.Println("  write failed:", err)
		return
	}
	fmt.Printf("  wired %s:\n", cfgPath)
	fmt.Println(`    + import gerrymander from "gerrymander-vite";`)
	fmt.Println(`    + plugins: [gerrymander(), …]`)
}

func viteConfigPath() string {
	for _, p := range []string{"vite.config.ts", "vite.config.js", "vite.config.mjs", "vite.config.mts"} {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func packageManager() string {
	switch {
	case fileExists("bun.lock") || fileExists("bun.lockb"):
		return "bun"
	case fileExists("pnpm-lock.yaml"):
		return "pnpm"
	case fileExists("yarn.lock"):
		return "yarn"
	default:
		return "npm"
	}
}

var pluginsArray = regexp.MustCompile(`(plugins\s*:\s*\[)`)

// wireViteConfig inserts the import and the plugin call. Confidence rules:
// there must be exactly one `plugins: [` occurrence, and the file must not
// already reference gerrymander.
func wireViteConfig(cfg string) (string, bool) {
	if len(pluginsArray.FindAllString(cfg, 2)) != 1 {
		return "", false
	}
	out := pluginsArray.ReplaceAllString(cfg, "${1}gerrymander(), ")

	imp := `import gerrymander from "gerrymander-vite";` + "\n"
	lines := strings.SplitAfter(out, "\n")
	lastImport := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "import ") {
			lastImport = i
		}
	}
	if lastImport >= 0 {
		lines[lastImport] += imp
		return strings.Join(lines, ""), true
	}
	return imp + out, true
}

// askYesNo prompts on the controlling terminal; anything but n/no is yes.
// Without a tty (CI, pipes) it declines, so scripts never get surprise
// edits.
func askYesNo(prompt string) bool {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		fmt.Print(prompt + "no tty — skipping\n")
		return false
	}
	defer tty.Close()
	fmt.Print(prompt)
	line, _ := bufio.NewReader(tty).ReadString('\n')
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans != "n" && ans != "no"
}

func printViteInstructions(pm, cfgPath string) {
	fmt.Printf("    %s add -D gerrymander-vite\n", pm)
	fmt.Printf("    // %s:\n", cfgPath)
	fmt.Println(`    import gerrymander from "gerrymander-vite";`)
	fmt.Println(`    plugins: [gerrymander(), /* …existing */],`)
}
