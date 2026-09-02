package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// cmdUpdate brings the gerry binary to the latest release. Homebrew
// installs defer to brew (so the formula stays the owner); direct installs
// self-replace atomically. `--check` reports without changing anything.
func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	check := fs.Bool("check", false, "report the latest version without installing")
	fs.Parse(args)

	latest, err := latestReleaseTag()
	if err != nil {
		return fmt.Errorf("could not determine the latest release: %w", err)
	}
	latestV := strings.TrimPrefix(latest, "v")
	if !semverNewer(latestV, version) {
		fmt.Printf("gerry %s is current\n", version)
		return nil
	}
	fmt.Printf("gerry %s → %s available\n", version, latestV)
	if *check {
		return nil
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.EvalSymlinks(self)

	// Homebrew owns its cellar; replacing the binary under it would fight
	// the package manager. Let brew do it.
	if strings.Contains(self, "/Cellar/") || strings.Contains(self, "/homebrew/") {
		fmt.Println("installed via Homebrew — updating through brew:")
		// brew only refreshes third-party taps on `brew update`, which it
		// skips when it ran recently — so an upgrade right after a release
		// sees the stale formula and says "already installed". Pull the
		// tap directly first; it's one tiny git repo.
		if repo, err := exec.Command("brew", "--repository", "nano112/tap").Output(); err == nil {
			exec.Command("git", "-C", strings.TrimSpace(string(repo)), "pull", "--quiet").Run()
		}
		cmd := exec.Command("brew", "upgrade", "nano112/tap/gerry")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		if runtime.GOOS == "linux" {
			updated, err := exec.LookPath("gerry")
			if err != nil {
				return fmt.Errorf("find Homebrew's updated gerry: %w", err)
			}
			updated, _ = filepath.EvalSymlinks(updated)
			if err := ensureLowPortCapability(updated); err != nil {
				return err
			}
		}
		return finishUpdate(latestV)
	}
	if runtime.GOOS == "windows" {
		return fmt.Errorf("self-update is not supported on Windows yet — download the %s zip from the releases page", latest)
	}

	url := fmt.Sprintf("https://github.com/Nano112/gerrymander/releases/download/%s/gerry_%s_%s_%s.tar.gz",
		latest, latestV, runtime.GOOS, runtime.GOARCH)
	fmt.Println("downloading", url)
	tmp, err := downloadBinary(url)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)

	// Preserve Linux file capabilities across the replacement. Renaming a
	// freshly downloaded inode over the executable otherwise silently drops
	// cap_net_bind_service and leaves the API healthy while DNS and the proxy
	// cannot bind 53/80/443.
	dst := self
	caps := fileCapabilities(dst)
	if runtime.GOOS == "linux" && hostServiceInstalled() && !hasLowPortCapability(caps) {
		caps = lowPortCapability
	}
	if err := replaceExecutable(tmp, dst, caps); err != nil {
		return err
	}
	return finishUpdate(latestV)
}

func finishUpdate(latestV string) error {
	fmt.Printf("updated to %s\n", latestV)
	if hostServiceInstalled() {
		if err := restartHostService(); err != nil {
			fmt.Println("the binary is updated, but the user service did not restart:")
			fmt.Println("  gerry service restart")
			return fmt.Errorf("restart host service: %w", err)
		}
		fmt.Println("restarted the host service")
	}
	return nil
}

const lowPortCapability = "cap_net_bind_service=ep"

// fileCapabilities returns the getcap expression attached to path. An empty
// string means either no capabilities or a platform without Linux file caps.
func fileCapabilities(path string) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	out, err := exec.Command("getcap", "-n", path).Output()
	if err != nil {
		return ""
	}
	return parseFileCapabilities(string(out))
}

func parseFileCapabilities(out string) string {
	line := strings.TrimSpace(out)
	if line == "" {
		return ""
	}
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return ""
	}
	for _, part := range parts[1:] {
		if strings.HasPrefix(part, "cap_") {
			return part
		}
	}
	return ""
}

func hasLowPortCapability(caps string) bool {
	return strings.Contains(caps, "cap_net_bind_service")
}

// ensureLowPortCapability makes a Linux host-service install immediately
// usable on the default DNS/HTTP/HTTPS ports. It is intentionally a no-op on
// other platforms and when the capability is already present.
func ensureLowPortCapability(path string) error {
	if runtime.GOOS != "linux" || hasLowPortCapability(fileCapabilities(path)) {
		return nil
	}
	fmt.Println("granting permission to bind DNS/HTTP/HTTPS ports (sudo)…")
	if err := runInteractive("sudo", "--", "setcap", lowPortCapability, path); err != nil {
		return fmt.Errorf("grant low-port capability: %w (install libcap/setcap, then retry)", err)
	}
	if !hasLowPortCapability(fileCapabilities(path)) {
		return fmt.Errorf("grant low-port capability: setcap completed but %s has no cap_net_bind_service", path)
	}
	return nil
}

// replaceExecutable stages the new inode beside dst, attaches any existing
// Linux capabilities to that inode, then renames it atomically. Root-owned
// install locations are elevated internally, keeping `gerry update` itself a
// normal user command so it can restart the correct systemd --user service.
func replaceExecutable(src, dst, caps string) error {
	staged := filepath.Join(filepath.Dir(dst), fmt.Sprintf(".gerry.new.%d", os.Getpid()))
	if err := copyFile(src, staged); err == nil {
		if caps != "" {
			if out, capErr := exec.Command("setcap", caps, staged).CombinedOutput(); capErr != nil {
				os.Remove(staged)
				return replaceExecutablePrivileged(src, staged, dst, caps, fmt.Errorf("setcap: %v: %s", capErr, strings.TrimSpace(string(out))))
			}
		}
		if err := os.Rename(staged, dst); err == nil {
			return nil
		} else if !os.IsPermission(err) {
			os.Remove(staged)
			return fmt.Errorf("replace %s: %w", dst, err)
		}
		os.Remove(staged)
	} else if !os.IsPermission(err) {
		return fmt.Errorf("stage next to %s: %w", dst, err)
	}
	return replaceExecutablePrivileged(src, staged, dst, caps, nil)
}

func replaceExecutablePrivileged(src, staged, dst, caps string, directErr error) error {
	steps := [][]string{{"install", "-m", "0755", src, staged}}
	if caps != "" {
		steps = append(steps, []string{"setcap", caps, staged})
	}
	steps = append(steps, []string{"mv", "-f", staged, dst})

	for _, step := range steps {
		if err := runInteractive("sudo", append([]string{"--"}, step...)...); err != nil {
			exec.Command("sudo", "--", "rm", "-f", staged).Run()
			if directErr != nil {
				return fmt.Errorf("replace %s: %w; privileged %s failed: %v", dst, directErr, step[0], err)
			}
			return fmt.Errorf("replace %s: privileged %s failed: %w", dst, step[0], err)
		}
	}
	return nil
}

func runInteractive(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// latestReleaseTag resolves the newest release WITHOUT the GitHub API:
// /releases/latest answers with a redirect whose Location ends in the tag.
// The API (60 unauthenticated requests/hour) is only a fallback, with
// GITHUB_TOKEN honored when present.
func latestReleaseTag() (string, error) {
	c := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if resp, err := c.Get("https://github.com/Nano112/gerrymander/releases/latest"); err == nil {
		loc := resp.Header.Get("Location")
		resp.Body.Close()
		if i := strings.LastIndex(loc, "/tag/"); i >= 0 {
			return loc[i+len("/tag/"):], nil
		}
	}

	api := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/Nano112/gerrymander/releases/latest", nil)
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := api.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("github: %s", resp.Status)
	}
	var out struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.TagName == "" {
		return "", fmt.Errorf("no releases found")
	}
	return out.TagName, nil
}

// downloadBinary fetches a release tarball and extracts the gerry binary to
// a temp file, returning its path.
func downloadBinary(url string) (string, error) {
	c := &http.Client{Timeout: 5 * time.Minute}
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download: %s", resp.Status)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("archive contained no gerry binary")
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasPrefix(filepath.Base(hdr.Name), "gerry") {
			continue
		}
		f, err := os.CreateTemp("", "gerry-update-*")
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			os.Remove(f.Name())
			return "", err
		}
		f.Close()
		os.Chmod(f.Name(), 0o755)
		return f.Name(), nil
	}
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o755)
}

// semverNewer reports a > b for x.y.z strings; a dev build ("dev") always
// counts as older, and a briefly stale release pointer can never suggest a
// downgrade.
func semverNewer(a, b string) bool {
	if b == "dev" {
		return true
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3 && i < len(pa) && i < len(pb); i++ {
		var na, nb int
		fmt.Sscanf(pa[i], "%d", &na)
		fmt.Sscanf(pb[i], "%d", &nb)
		if na != nb {
			return na > nb
		}
	}
	return false
}
