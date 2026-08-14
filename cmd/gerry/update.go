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
		return cmd.Run()
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

	// Atomic replace: rename into place; cross-device or permission
	// failures fall back to an explicit instruction rather than a half
	// -written binary.
	dst := self
	staged := filepath.Join(filepath.Dir(dst), ".gerry.new")
	if err := copyFile(tmp, staged); err != nil {
		return fmt.Errorf("stage next to %s: %w (need sudo? run: sudo gerry update)", dst, err)
	}
	if err := os.Rename(staged, dst); err != nil {
		os.Remove(staged)
		return fmt.Errorf("replace %s: %w (need sudo? run: sudo gerry update)", dst, err)
	}
	fmt.Printf("updated to %s. Daemons keep running the old code until restarted:\n", latestV)
	fmt.Println("  gerry service restart   # host mode")
	return nil
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
