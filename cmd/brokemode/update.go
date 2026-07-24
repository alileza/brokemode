package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/alileza/brokemode/internal/ollama"
)

func releaseRepo() string {
	if r := os.Getenv("BROKEMODE_REPO"); r != "" {
		return r
	}
	return "alileza/brokemode"
}

func releaseHost() string {
	if h := os.Getenv("BROKEMODE_RELEASE_HOST"); h != "" {
		return h
	}
	return "https://github.com"
}

// latestTag resolves the newest release tag without the rate-limited API:
// GitHub answers /releases/latest with a redirect to /releases/tag/vX.Y.Z.
func latestTag(host, repo string) (string, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(host + "/" + repo + "/releases/latest")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("no releases published yet for %s", repo)
	}
	tag := loc[strings.LastIndexByte(loc, '/')+1:]
	if tag == "" || tag == "latest" {
		return "", fmt.Errorf("could not resolve the latest release tag from %s", loc)
	}
	return tag, nil
}

// selfUpdate downloads the release binary for this OS/arch and atomically
// replaces targetPath. Returns the tag it installed.
func selfUpdate(targetPath, host, repo, tag string) error {
	url := fmt.Sprintf("%s/%s/releases/download/%s/brokemode-%s-%s", host, repo, tag, runtime.GOOS, runtime.GOARCH)
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Get(url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	// Same directory as the target so the final rename is atomic.
	tmp, err := os.CreateTemp(filepath.Dir(targetPath), ".brokemode-update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	// The new binary must at least run before it replaces us.
	if out, err := exec.Command(tmpPath, "--version").Output(); err != nil {
		return fmt.Errorf("downloaded binary failed its smoke test: %v", err)
	} else if !strings.Contains(string(out), "brokemode") {
		return fmt.Errorf("downloaded binary does not look like brokemode: %q", strings.TrimSpace(string(out)))
	}
	return os.Rename(tmpPath, targetPath)
}

// updateTarget is the real binary path (through the brew-bin symlink).
func updateTarget() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func runUpdate(force bool) error {
	host, repo := releaseHost(), releaseRepo()
	tag, err := latestTag(host, repo)
	if err != nil {
		return err
	}
	if !force && version != "dev" && ollama.CompareVersions(tag, version) <= 0 {
		fmt.Printf("brokemode %s is already the latest release\n", version)
		return nil
	}
	target, err := updateTarget()
	if err != nil {
		return err
	}
	fmt.Printf("updating %s: %s -> %s\n", target, version, tag)
	if err := selfUpdate(target, host, repo, tag); err != nil {
		return err
	}
	fmt.Printf("updated to brokemode %s\n", tag)
	return nil
}

func newUpdateCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Self-update to the latest release",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even when already on the latest release")
	return cmd
}
