// Package selfupdate implements `harborrs update`: download the latest
// release matching the current GOOS/GOARCH from GitHub Releases, verify
// its sha256 against checksums.txt, and atomically replace the running
// binary.
//
// Design notes:
//
//   - The download is staged next to the running binary (same directory
//     → same filesystem) so the final os.Rename is atomic.
//   - Linux and macOS both permit renaming over a running executable;
//     the kernel keeps the old inode mapped for the live process.
//   - Self-update is refused when the binary lives under a path managed
//     by a package manager (Homebrew, linuxbrew). The user is told to
//     use the package manager instead.
//   - No third-party dependencies. Pure stdlib.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultRepo is the github "owner/name" we update from.
const DefaultRepo = "kfet/harb"

// Options configures a single update run.
type Options struct {
	Repo       string // owner/repo (default kfet/harb)
	Version    string // explicit tag like "v0.2.0"; "" → latest
	CheckOnly  bool   // resolve + report, do not download or replace
	HTTPClient *http.Client
	Stdout     io.Writer
	Stderr     io.Writer
	// ExecPath overrides os.Executable() — for tests.
	ExecPath func() (string, error)
}

// managedPaths are prefixes we refuse to self-update under. Each entry
// must be matched as a path prefix on the resolved (symlink-followed)
// executable path.
var managedPaths = []string{
	"/opt/homebrew/",
	"/usr/local/Cellar/",
	"/usr/local/Homebrew/",
	"/home/linuxbrew/",
	"/usr/bin/",
	"/usr/sbin/",
}

// Run performs the update according to opts. Returns nil on success or
// "no update needed". Returns a non-nil error if the update failed.
func Run(currentVersion string, opts Options) error {
	if opts.Repo == "" {
		opts.Repo = DefaultRepo
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.ExecPath == nil {
		opts.ExecPath = os.Executable
	}

	exe, err := opts.ExecPath()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	for _, p := range managedPaths {
		if strings.HasPrefix(resolved, p) {
			return fmt.Errorf("refusing to self-update: %s is managed by a package manager; use the package manager (e.g. `brew upgrade %s`) instead", resolved, opts.Repo)
		}
	}

	// 1. Resolve target version.
	target := opts.Version
	if target == "" {
		target, err = resolveLatest(opts.HTTPClient, opts.Repo)
		if err != nil {
			return fmt.Errorf("resolve latest: %w", err)
		}
	}
	target = ensureV(target)
	cur := ensureV(currentVersion)

	// 2. Compare with current.
	fmt.Fprintf(opts.Stdout, "current: %s\nlatest:  %s\n", cur, target)
	if target == cur {
		fmt.Fprintln(opts.Stdout, "already up to date.")
		return nil
	}
	if opts.CheckOnly {
		fmt.Fprintln(opts.Stdout, "update available. run `harborrs update` to install.")
		return nil
	}

	// 3. Download + verify.
	verNoV := strings.TrimPrefix(target, "v")
	asset := fmt.Sprintf("harborrs-%s-%s-%s.tar.gz", verNoV, runtime.GOOS, assetArch(runtime.GOARCH))
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s", opts.Repo, target)
	fmt.Fprintf(opts.Stdout, "downloading %s/%s\n", base, asset)

	tmpDir, err := os.MkdirTemp(filepath.Dir(resolved), ".harborrs-update-*")
	if err != nil {
		return fmt.Errorf("tempdir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tarPath := filepath.Join(tmpDir, asset)
	if err := download(opts.HTTPClient, base+"/"+asset, tarPath); err != nil {
		return fmt.Errorf("download asset: %w", err)
	}

	sumsPath := filepath.Join(tmpDir, "checksums.txt")
	if err := download(opts.HTTPClient, base+"/checksums.txt", sumsPath); err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}

	expected, err := lookupChecksum(sumsPath, asset)
	if err != nil {
		return err
	}
	actual, err := sha256File(tarPath)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expected, actual)
	}
	fmt.Fprintln(opts.Stdout, "✓ checksum verified")

	// 4. Extract binary.
	newBin := filepath.Join(tmpDir, "harborrs.new")
	if err := extractBinary(tarPath, newBin); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	if err := os.Chmod(newBin, 0o755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	// 5. Atomic rename. tmpDir is alongside resolved, so same filesystem.
	if err := os.Rename(newBin, resolved); err != nil {
		return fmt.Errorf("replace: %w", err)
	}
	fmt.Fprintf(opts.Stdout, "✓ harborrs updated: %s → %s at %s\n", cur, target, resolved)
	return nil
}

// ensureV prepends "v" to a version string if not already present.
func ensureV(s string) string {
	if strings.HasPrefix(s, "v") {
		return s
	}
	return "v" + s
}

// resolveLatest returns the tag of the latest GitHub release of repo
// (e.g. "v0.2.0"). Uses the unauthenticated GitHub API.
func resolveLatest(c *http.Client, repo string) (string, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("github api: %s", resp.Status)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	if rel.TagName == "" {
		return "", errors.New("github api: empty tag_name")
	}
	return rel.TagName, nil
}

func download(c *http.Client, src, dst string) error {
	resp, err := c.Get(src)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: %s", src, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return f.Sync()
}

func lookupChecksum(sumsPath, asset string) (string, error) {
	data, err := os.ReadFile(sumsPath)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum entry for %s", asset)
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractBinary pulls the "harborrs" file out of a release tarball
// (which is laid out as "harborrs-<ver>-<os>-<arch>/harborrs") and
// writes it to dst.
func extractBinary(tarPath, dst string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if path.Base(hdr.Name) != "harborrs" || hdr.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.Create(dst)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
	return errors.New("harborrs binary not found in archive")
}

// assetArch maps runtime.GOARCH to the arch suffix used in release asset
// names. GOARCH=arm (with GOARM=6 in our matrix) is published as "armv6".
func assetArch(goarch string) string {
	if goarch == "arm" {
		return "armv6"
	}
	return goarch
}
