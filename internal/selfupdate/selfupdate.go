package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/trustos/hopssh/internal/buildinfo"
)

const (
	githubRepo        = "trustos/hopssh"
	githubReleasesURL = "https://api.github.com/repos/" + githubRepo + "/releases/latest"
	githubDownloadURL = "https://github.com/" + githubRepo + "/releases/download"
)

// Release describes an available update.
type Release struct {
	Version string
	Source  string // "control-plane" or "github"
}

// Check queries the control plane (or GitHub as fallback) for the latest version.
// Returns nil if already up to date.
func Check(component, endpoint string) (*Release, error) {
	// Try control plane first.
	if endpoint != "" {
		release, err := checkControlPlane(endpoint)
		if err == nil {
			if release.Version == buildinfo.Version {
				return nil, nil
			}
			return release, nil
		}
		// Fall through to GitHub.
	}

	release, err := checkGitHub()
	if err != nil {
		if endpoint != "" {
			return nil, fmt.Errorf("cannot reach control plane at %s and GitHub API also failed: %w\nCheck network connectivity", endpoint, err)
		}
		return nil, fmt.Errorf("cannot reach GitHub API: %w\nIf rate limited, set GITHUB_TOKEN env var or try again later.\nCurrent version: %s", err, buildinfo.Version)
	}
	if release.Version == buildinfo.Version {
		return nil, nil
	}
	return release, nil
}

func checkControlPlane(endpoint string) (*Release, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(endpoint + "/version")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	return &Release{Version: v.Version, Source: "control-plane"}, nil
}

func checkGitHub() (*Release, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", githubReleasesURL, nil)
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 {
		return nil, fmt.Errorf("GitHub API rate limited (HTTP 403)")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}
	return &Release{Version: release.TagName, Source: "github"}, nil
}

// Apply downloads and installs the update. The current binary is atomically replaced.
func Apply(component string, release *Release, endpoint string) error {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	binaryName := fmt.Sprintf("hop-%s-%s-%s%s", component, goos, goarch, ext)

	// Determine download URL.
	var binaryURL, checksumsURL string
	if release.Source == "control-plane" && endpoint != "" {
		binaryURL = endpoint + "/download/" + binaryName
		checksumsURL = endpoint + "/download/SHA256SUMS"
	} else {
		binaryURL = fmt.Sprintf("%s/%s/%s", githubDownloadURL, release.Version, binaryName)
		checksumsURL = fmt.Sprintf("%s/%s/SHA256SUMS", githubDownloadURL, release.Version)
	}

	// Find current binary path.
	currentPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine current binary path: %w", err)
	}
	currentPath, err = filepath.EvalSymlinks(currentPath)
	if err != nil {
		return fmt.Errorf("cannot resolve binary path: %w", err)
	}

	// Download to temp file in same directory (for atomic rename).
	dir := filepath.Dir(currentPath)
	tmpFile, err := os.CreateTemp(dir, "hop-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w\nRun with sudo: sudo hop-%s update", dir, err, component)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // cleanup on failure

	fmt.Printf("==> Downloading %s %s...\n", component, release.Version)
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(binaryURL)
	if err != nil {
		return fmt.Errorf("download failed: %w\nCheck that the version %s exists", err, release.Version)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed (HTTP %d) from %s\nCheck that the version %s exists", resp.StatusCode, binaryURL, release.Version)
	}

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		return fmt.Errorf("download interrupted: %w", err)
	}
	tmpFile.Close()

	// Verify checksum.
	if err := verifyChecksum(tmpPath, binaryName, checksumsURL, client); err != nil {
		return err
	}

	// Set executable permissions.
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("chmod failed: %w", err)
	}

	// Atomic replace.
	if err := os.Rename(tmpPath, currentPath); err != nil {
		return fmt.Errorf("cannot replace binary at %s: %w\nRun with sudo: sudo hop-%s update", currentPath, err, component)
	}

	fmt.Printf("==> Updated hop-%s: %s → %s\n", component, buildinfo.Version, release.Version)

	// Restart service if running as systemd/launchd.
	restartService(component)
	return nil
}

func verifyChecksum(filePath, binaryName, checksumsURL string, client *http.Client) error {
	resp, err := client.Get(checksumsURL)
	if err != nil || resp.StatusCode != 200 {
		// Checksums not available — skip verification with warning.
		fmt.Println("    Warning: Could not fetch checksums, skipping verification.")
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil // skip on read error
	}

	// Find expected checksum for our binary.
	var expected string
	for _, line := range strings.Split(string(body), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == binaryName {
			expected = parts[0]
			break
		}
	}
	if expected == "" {
		fmt.Println("    Warning: Binary not found in checksums file, skipping verification.")
		return nil
	}

	// Compute actual checksum.
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("cannot read downloaded file for checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("checksum computation failed: %w", err)
	}
	actual := hex.EncodeToString(h.Sum(nil))

	if actual != expected {
		return fmt.Errorf("checksum verification failed for %s\n  Expected: %s\n  Got:      %s\nThe download may be corrupted. Aborting update — current binary unchanged", binaryName, expected, actual)
	}
	fmt.Println("    Checksum verified.")
	return nil
}

func restartService(component string) {
	var serviceName string
	switch component {
	case "agent":
		serviceName = "hop-agent"
	case "server":
		serviceName = "hopssh"
	default:
		return
	}

	// Try systemd.
	if _, err := exec.LookPath("systemctl"); err == nil {
		// Check if the service exists and is active.
		if err := exec.Command("systemctl", "is-active", "--quiet", serviceName).Run(); err == nil {
			fmt.Printf("==> Restarting %s service...\n", serviceName)
			if out, err := exec.Command("systemctl", "restart", serviceName).CombinedOutput(); err != nil {
				fmt.Printf("    Warning: Failed to restart service: %v\n%s", err, out)
				fmt.Printf("    Restart manually: sudo systemctl restart %s\n", serviceName)
			} else {
				fmt.Println("    Service restarted.")
			}
			return
		}
	}

	// Try launchd.
	if _, err := exec.LookPath("launchctl"); err == nil {
		label := "com.hopssh." + component
		// Check if loaded.
		if err := exec.Command("launchctl", "list", label).Run(); err == nil {
			home, _ := os.UserHomeDir()
			var plistPath string
			switch component {
			case "agent":
				plistPath = filepath.Join(home, "Library/LaunchAgents/com.hopssh.agent.plist")
			case "server":
				plistPath = filepath.Join(home, "Library/LaunchAgents/com.hopssh.server.plist")
			}
			if plistPath != "" {
				exec.Command("launchctl", "unload", plistPath).Run()
				exec.Command("launchctl", "load", plistPath).Run()
				fmt.Println("    Service restarted.")
			}
			return
		}
	}
}
