package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alileza/brokemode/internal/ollama"
	"github.com/alileza/brokemode/internal/registry"
)

// fakeOllama serves /api/generate and /api/chat with deterministic timing
// fields; decodeTPS can decay per call to simulate thermal throttling.
type fakeOllama struct {
	generateCalls atomic.Int64
	chatCalls     atomic.Int64
	decayPerCall  float64 // fraction knocked off decode rate per generate call
}

const baseEvalCount = 100

func (f *fakeOllama) timing(call int64) map[string]any {
	rate := 20.0 * (1 - f.decayPerCall*float64(call))
	if rate < 1 {
		rate = 1
	}
	evalNs := int64(float64(baseEvalCount) / rate * float64(time.Second))
	return map[string]any{
		"total_duration":       int64(500*time.Millisecond) + evalNs,
		"load_duration":        int64(80 * time.Millisecond),
		"prompt_eval_count":    42,
		"prompt_eval_duration": int64(120 * time.Millisecond),
		"eval_count":           baseEvalCount,
		"eval_duration":        evalNs,
	}
}

func (f *fakeOllama) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/generate", func(w http.ResponseWriter, r *http.Request) {
		var req ollama.GenerateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if req.Stream {
			http.Error(w, "bench must use stream:false", 400)
			return
		}
		if req.Options == nil || req.Options.NumCtx == 0 {
			http.Error(w, "num_ctx not applied", 400)
			return
		}
		call := f.generateCalls.Add(1) - 1
		resp := f.timing(call)
		resp["model"] = req.Model
		resp["response"] = "ok"
		resp["done"] = true
		resp["done_reason"] = "stop"
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		var req ollama.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		call := f.chatCalls.Add(1)
		resp := f.timing(0)
		resp["model"] = req.Model
		resp["done"] = true
		// First leg: emit a tool call. Second leg (a tool message present):
		// final answer.
		hasToolResult := false
		for _, m := range req.Messages {
			if m.Role == "tool" {
				hasToolResult = true
			}
		}
		if !hasToolResult && len(req.Tools) > 0 {
			resp["message"] = map[string]any{
				"role": "assistant", "content": "",
				"tool_calls": []map[string]any{{
					"function": map[string]any{"name": "get_weather", "arguments": map[string]any{"city": "Innsbruck"}},
				}},
			}
			resp["done_reason"] = "tool_calls"
		} else {
			resp["message"] = map[string]any{"role": "assistant", "content": fmt.Sprintf("final answer %d", call)}
			resp["done_reason"] = "stop"
		}
		json.NewEncoder(w).Encode(resp)
	})
	return mux
}

func testRegistry(t *testing.T) (*registry.Registry, *registry.Model) {
	t.Helper()
	r, err := registry.Parse([]byte(`
max_rss_gb: 11
models:
  - name: testmodel:1b
    num_ctx: 4096
    peak_rss_gb: 2.0
`))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := r.Resolve("testmodel:1b")
	return r, m
}

func newRunner(t *testing.T, srv *httptest.Server, outDir string) *Runner {
	t.Helper()
	reg, model := testRegistry(t)
	return &Runner{
		Client:   ollama.New(srv.URL),
		Registry: reg,
		Model:    model,
		Warmups:  1,
		Measured: 3,
		OutDir:   outDir,
		SudoOK:   false,
		Now:      func() time.Time { return time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC) },
	}
}

func TestRunSuiteAgainstFakeOllama(t *testing.T) {
	fake := &fakeOllama{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	out := t.TempDir()
	r := newRunner(t, srv, out)
	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Cases) != len(Suite) {
		t.Fatalf("got %d cases, want %d", len(report.Cases), len(Suite))
	}
	if !report.Partial {
		t.Error("run without sudo must be marked partial")
	}
	for _, c := range report.Cases {
		if len(c.Runs) != 3 {
			t.Errorf("%s: %d measured runs, want 3", c.Case, len(c.Runs))
		}
		if c.DecodeTPS.Median < 19 || c.DecodeTPS.Median > 21 {
			t.Errorf("%s: median decode %.1f, want ~20", c.Case, c.DecodeTPS.Median)
		}
		if c.TTFTMs.Median != 200 { // 80ms load + 120ms prompt eval
			t.Errorf("%s: median TTFT %.0f, want 200", c.Case, c.TTFTMs.Median)
		}
	}

	// Tool-call case must aggregate both legs: 2 * 42 tokens in per run.
	tc := report.Cases[len(report.Cases)-1]
	if tc.Case != "tool-call" {
		t.Fatalf("last case is %s, want tool-call", tc.Case)
	}
	if tc.Runs[0].TokensIn != 84 {
		t.Errorf("tool-call TokensIn=%d, want 84 (two legs)", tc.Runs[0].TokensIn)
	}
	if got := fake.chatCalls.Load(); got != 8 { // 4 runs x 2 legs
		t.Errorf("chat calls=%d, want 8", got)
	}

	// Report and summary files exist.
	if _, err := os.Stat(filepath.Join(out, "testmodel_1b-20260724-120000.json")); err != nil {
		t.Error(err)
	}
	sum, err := os.ReadFile(filepath.Join(out, "summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sum), "testmodel:1b") || !strings.Contains(string(sum), "short-chat") {
		t.Errorf("summary.md missing expected rows:\n%s", sum)
	}
}

func TestThermalThrottleDetection(t *testing.T) {
	// 12% decay per generate call: by the last measured run decode is far
	// more than 15% below the first.
	fake := &fakeOllama{decayPerCall: 0.12}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	r := newRunner(t, srv, "")
	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Cases[0].ThermalThrottle {
		t.Error("expected thermal throttle flag on decaying decode rate")
	}
}

func TestBudgetRefusal(t *testing.T) {
	fake := &fakeOllama{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	reg, err := registry.Parse([]byte(`
max_rss_gb: 5
models:
  - name: big:70b
    num_ctx: 4096
    peak_rss_gb: 40
`))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := reg.Resolve("big:70b")
	r := &Runner{Client: ollama.New(srv.URL), Registry: reg, Model: m}
	if _, err := r.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "REFUSING") {
		t.Fatalf("want loud budget refusal, got %v", err)
	}
	if fake.generateCalls.Load() != 0 {
		t.Error("no requests may reach ollama for an over-budget model")
	}
}

func TestPercentile(t *testing.T) {
	vals := []float64{5, 1, 4, 2, 3}
	if got := percentile(vals, 50); got != 3 {
		t.Errorf("median=%v, want 3", got)
	}
	if got := percentile(vals, 95); got != 5 {
		t.Errorf("p95=%v, want 5", got)
	}
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("empty percentile=%v, want 0", got)
	}
}
