package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Version calls GET /api/version and returns the running server's version
// (e.g. "0.9.2").
func (c *Client) Version(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama /api/version: %w (is ollama running?)", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama /api/version: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode /api/version response: %w", err)
	}
	return out.Version, nil
}

// CompareVersions compares two dotted version strings numerically,
// ignoring any leading "v" and pre-release suffixes ("0.9.2-rc1" -> 0.9.2).
// Returns -1, 0, or 1.
func CompareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		if i >= 3 {
			break
		}
		// Non-numeric segments count as 0 rather than failing.
		n, _ := strconv.Atoi(strings.TrimSpace(part))
		out[i] = n
	}
	return out
}

// CheckServerVersion fetches the server version and compares it to min.
// It returns (serverVersion, nil) when the server satisfies min, and a
// descriptive error otherwise. An empty min always passes.
func (c *Client) CheckServerVersion(ctx context.Context, min string) (string, error) {
	if min == "" {
		return "", nil
	}
	v, err := c.Version(ctx)
	if err != nil {
		return "", err
	}
	if CompareVersions(v, min) < 0 {
		return v, fmt.Errorf("ollama server v%s is older than the required v%s — run: brew upgrade ollama && brew services restart ollama", v, min)
	}
	return v, nil
}
