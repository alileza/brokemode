package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.9.0", "0.9.0", 0},
		{"0.8.9", "0.9.0", -1},
		{"0.10.0", "0.9.0", 1},
		{"1.0.0", "0.99.99", 1},
		{"v0.9.1", "0.9.0", 1},
		{"0.9.2-rc1", "0.9.2", 0},
		{"0.9", "0.9.0", 0},
		{"garbage", "0.0.1", -1},
	}
	for _, tt := range tests {
		if got := CompareVersions(tt.a, tt.b); got != tt.want {
			t.Errorf("CompareVersions(%q,%q)=%d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func versionServer(v string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"version": v})
	})
	return httptest.NewServer(mux)
}

func TestCheckServerVersion(t *testing.T) {
	srv := versionServer("0.8.1")
	defer srv.Close()
	c := New(srv.URL)

	// Too old: descriptive error naming both versions and the fix.
	got, err := c.CheckServerVersion(context.Background(), "0.9.0")
	if err == nil {
		t.Fatal("expected version error")
	}
	if got != "0.8.1" || !strings.Contains(err.Error(), "brew upgrade ollama") {
		t.Errorf("got version %q, err %v", got, err)
	}

	// Satisfied.
	if v, err := c.CheckServerVersion(context.Background(), "0.8.0"); err != nil || v != "0.8.1" {
		t.Errorf("satisfied check: v=%q err=%v", v, err)
	}

	// No minimum: always passes without a request.
	if _, err := c.CheckServerVersion(context.Background(), ""); err != nil {
		t.Errorf("empty min must pass: %v", err)
	}
}
