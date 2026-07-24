// Package bench drives Ollama with the embedded prompt suite and derives
// throughput metrics from Ollama's native timing fields, while sampling
// host telemetry at 1Hz.
package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/alileza/brokemode/bench/prompts"
	"github.com/alileza/brokemode/internal/ollama"
	"github.com/alileza/brokemode/internal/registry"
	"github.com/alileza/brokemode/internal/telemetry"
)

// Kind selects how a case is driven.
type Kind string

// Case kinds: plain /api/generate, JSON-forced output, or a two-leg
// tool-call round trip over /api/chat.
const (
	KindGenerate Kind = "generate"
	KindJSON     Kind = "json"
	KindToolCall Kind = "tool_call"
)

// Case is one prompt-suite entry.
type Case struct {
	Name       string
	PromptFile string
	Kind       Kind
	NumPredict int
}

// Suite is the standard prompt suite, in execution order.
var Suite = []Case{
	{Name: "short-chat", PromptFile: "short-chat.txt", Kind: KindGenerate, NumPredict: 256},
	{Name: "summarize-1k", PromptFile: "summarize-1k.txt", Kind: KindGenerate, NumPredict: 256},
	{Name: "code-review-4k", PromptFile: "code-review-4k.txt", Kind: KindGenerate, NumPredict: 512},
	{Name: "json-structured", PromptFile: "json-structured.txt", Kind: KindJSON, NumPredict: 512},
	{Name: "tool-call", PromptFile: "tool-call.txt", Kind: KindToolCall, NumPredict: 256},
}

// RunMetrics are the derived numbers for one run, all from Ollama's native
// timing fields (load_duration, prompt_eval_*, eval_*).
type RunMetrics struct {
	TTFTMs     float64 `json:"ttft_ms"`     // load + prompt eval
	PrefillTPS float64 `json:"prefill_tps"` // prompt_eval_count / prompt_eval_duration
	DecodeTPS  float64 `json:"decode_tps"`  // eval_count / eval_duration
	WallMs     float64 `json:"wall_ms"`     // total_duration
	LoadMs     float64 `json:"load_ms"`     // load_duration
	TokensIn   int     `json:"tokens_in"`   // prompt_eval_count
	TokensOut  int     `json:"tokens_out"`  // eval_count
}

// Stat is a median/p95 pair. Never the mean.
type Stat struct {
	Median float64 `json:"median"`
	P95    float64 `json:"p95"`
}

// TelemetrySummary is the peak host state observed during a case's runs.
type TelemetrySummary struct {
	PeakOllamaRSSBytes  uint64  `json:"peak_ollama_rss_bytes"`
	PeakGPUActiveRatio  float64 `json:"peak_gpu_active_ratio"`
	PeakPackagePowerW   float64 `json:"peak_package_power_w"`
	PeakMemoryPressure  float64 `json:"peak_memory_pressure_pct"`
	PeakThermalLevel    int     `json:"peak_thermal_level"`
	PeakCompressedBytes uint64  `json:"peak_compressed_bytes"`
	Samples             int     `json:"samples"`
}

// CaseResult is one prompt-suite case's full output.
type CaseResult struct {
	Case            string           `json:"case"`
	Kind            Kind             `json:"kind"`
	Runs            []RunMetrics     `json:"runs"` // measured runs only
	TTFTMs          Stat             `json:"ttft_ms"`
	PrefillTPS      Stat             `json:"prefill_tps"`
	DecodeTPS       Stat             `json:"decode_tps"`
	WallMs          Stat             `json:"wall_ms"`
	TokensOutTotal  int              `json:"tokens_out_total"`
	Telemetry       TelemetrySummary `json:"telemetry"`
	ThermalThrottle bool             `json:"thermal_throttle_suspected"`
}

// Report is the top-level JSON written to results/<model>-<timestamp>.json.
type Report struct {
	Model     string       `json:"model"`
	Tag       string       `json:"ollama_tag"`
	NumCtx    int          `json:"num_ctx"`
	Timestamp string       `json:"timestamp"`
	Warmups   int          `json:"warmups"`
	Measured  int          `json:"measured_runs"`
	Partial   bool         `json:"partial"` // telemetry incomplete (e.g. no sudo)
	Warnings  []string     `json:"warnings,omitempty"`
	Cases     []CaseResult `json:"cases"`
}

// Runner executes the suite against one model.
type Runner struct {
	Client   *ollama.Client
	Registry *registry.Registry
	Model    *registry.Model
	Warmups  int
	Measured int
	OutDir   string
	Log      io.Writer
	// SudoOK enables powermetrics; when false the run is marked partial.
	SudoOK bool
	// PromptDir overrides the embedded suite (for testing / custom prompts).
	PromptDir string
	// now is injectable for tests.
	Now func() time.Time
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		_, _ = fmt.Fprintf(r.Log, format+"\n", args...)
	}
}

func (r *Runner) prompt(c Case) (string, error) {
	if r.PromptDir != "" {
		b, err := os.ReadFile(filepath.Join(r.PromptDir, c.PromptFile))
		return string(b), err
	}
	b, err := prompts.FS.ReadFile(c.PromptFile)
	return string(b), err
}

// Run executes the whole suite and writes the JSON report and summary.md.
func (r *Runner) Run(ctx context.Context) (*Report, error) {
	if err := r.Registry.CheckBudget(r.Model); err != nil {
		return nil, err
	}
	if r.Warmups == 0 {
		r.Warmups = 3
	}
	if r.Measured == 0 {
		r.Measured = 5
	}
	if r.Now == nil {
		r.Now = time.Now
	}

	report := &Report{
		Model:     r.Model.Name,
		Tag:       r.Model.Name,
		NumCtx:    r.Model.NumCtx,
		Timestamp: r.Now().UTC().Format("20060102-150405"),
		Warmups:   r.Warmups,
		Measured:  r.Measured,
	}

	if !r.SudoOK {
		report.Partial = true
		w := "sudo unavailable: GPU residency and package power will NOT be sampled — run `sudo -v` first for full telemetry. Results are marked PARTIAL."
		report.Warnings = append(report.Warnings, w)
		r.logf("\n!!! WARNING: %s\n", w)
	}

	for _, c := range Suite {
		res, err := r.runCase(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", c.Name, err)
		}
		report.Cases = append(report.Cases, *res)
		if res.ThermalThrottle {
			r.logf("\n!!! THERMAL THROTTLE SUSPECTED on %s: decode rate of the last run is >15%% below the first (%.1f vs %.1f tok/s). Let the machine cool down and re-run.\n",
				c.Name, res.Runs[len(res.Runs)-1].DecodeTPS, res.Runs[0].DecodeTPS)
		}
	}

	if r.OutDir != "" {
		if err := r.write(report); err != nil {
			return report, err
		}
	}
	return report, nil
}

func (r *Runner) runCase(ctx context.Context, c Case) (*CaseResult, error) {
	promptText, err := r.prompt(c)
	if err != nil {
		return nil, fmt.Errorf("load prompt: %w", err)
	}
	r.logf("== %s (%s): %d warmup + %d measured", c.Name, c.Kind, r.Warmups, r.Measured)

	// Telemetry runs for the duration of the case at 1Hz.
	collector := telemetry.NewCollector(r.SudoOK)
	tctx, tcancel := context.WithCancel(ctx)
	defer tcancel()
	var summary TelemetrySummary
	done := make(chan struct{})
	go func() {
		defer close(done)
		collector.Run(tctx, func(s telemetry.Snapshot) {
			summary.Samples++
			summary.PeakOllamaRSSBytes = max(summary.PeakOllamaRSSBytes, s.OllamaRSSBytes)
			summary.PeakGPUActiveRatio = maxF(summary.PeakGPUActiveRatio, s.GPUActiveRatio)
			summary.PeakPackagePowerW = maxF(summary.PeakPackagePowerW, s.PackagePowerW)
			summary.PeakMemoryPressure = maxF(summary.PeakMemoryPressure, s.MemoryPressurePct)
			summary.PeakCompressedBytes = max(summary.PeakCompressedBytes, s.CompressedBytes)
			if s.ThermalLevel > summary.PeakThermalLevel {
				summary.PeakThermalLevel = s.ThermalLevel
			}
		})
	}()

	res := &CaseResult{Case: c.Name, Kind: c.Kind}
	for i := 0; i < r.Warmups+r.Measured; i++ {
		m, err := r.oneRun(ctx, c, promptText)
		if err != nil {
			return nil, err
		}
		if i < r.Warmups {
			r.logf("   warmup %d/%d: %.1f tok/s decode", i+1, r.Warmups, m.DecodeTPS)
			continue
		}
		res.Runs = append(res.Runs, *m)
		res.TokensOutTotal += m.TokensOut
		r.logf("   run %d/%d: TTFT %.0fms | prefill %.1f tok/s | decode %.1f tok/s | %d tok out",
			i-r.Warmups+1, r.Measured, m.TTFTMs, m.PrefillTPS, m.DecodeTPS, m.TokensOut)
	}

	tcancel()
	<-done
	res.Telemetry = summary

	res.TTFTMs = statOf(res.Runs, func(m RunMetrics) float64 { return m.TTFTMs })
	res.PrefillTPS = statOf(res.Runs, func(m RunMetrics) float64 { return m.PrefillTPS })
	res.DecodeTPS = statOf(res.Runs, func(m RunMetrics) float64 { return m.DecodeTPS })
	res.WallMs = statOf(res.Runs, func(m RunMetrics) float64 { return m.WallMs })

	if n := len(res.Runs); n >= 2 {
		first, last := res.Runs[0].DecodeTPS, res.Runs[n-1].DecodeTPS
		if first > 0 && last < first*0.85 {
			res.ThermalThrottle = true
		}
	}
	return res, nil
}

func (r *Runner) oneRun(ctx context.Context, c Case, promptText string) (*RunMetrics, error) {
	opts := &ollama.Options{NumCtx: r.Model.NumCtx, NumPredict: c.NumPredict}
	switch c.Kind {
	case KindToolCall:
		return r.toolCallRoundTrip(ctx, promptText, opts)
	case KindJSON, KindGenerate:
		req := ollama.GenerateRequest{Model: r.Model.Name, Prompt: promptText, Options: opts}
		if c.Kind == KindJSON {
			req.Format = "json"
		}
		resp, err := r.Client.Generate(ctx, req)
		if err != nil {
			return nil, err
		}
		return fromTiming(resp.Timing), nil
	default:
		return nil, fmt.Errorf("unknown case kind %q", c.Kind)
	}
}

// weatherTool is the canned tool declared for the tool-call round trip.
var weatherTool = ollama.Tool{
	Type: "function",
	Function: ollama.ToolFunction{
		Name:        "get_weather",
		Description: "Get current weather for a city",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
	},
}

// toolCallRoundTrip does chat -> tool_call -> tool_result -> final answer,
// aggregating the timing of both legs. TTFT comes from the first leg.
func (r *Runner) toolCallRoundTrip(ctx context.Context, promptText string, opts *ollama.Options) (*RunMetrics, error) {
	msgs := []ollama.Message{{Role: "user", Content: promptText}}
	first, err := r.Client.Chat(ctx, ollama.ChatRequest{
		Model: r.Model.Name, Messages: msgs, Tools: []ollama.Tool{weatherTool}, Options: opts,
	})
	if err != nil {
		return nil, err
	}
	m := fromTiming(first.Timing)
	if len(first.Message.ToolCalls) == 0 {
		// Model answered without calling the tool; still a valid timing
		// sample, but note it so quality regressions are visible.
		r.logf("   note: model skipped the tool call this run")
		return m, nil
	}

	msgs = append(msgs, first.Message)
	for _, tc := range first.Message.ToolCalls {
		msgs = append(msgs, ollama.Message{
			Role:     "tool",
			ToolName: tc.Function.Name,
			Content:  `{"city":"Innsbruck","temp_c":11,"conditions":"showers","chance_of_rain_pct":80}`,
		})
	}
	second, err := r.Client.Chat(ctx, ollama.ChatRequest{
		Model: r.Model.Name, Messages: msgs, Tools: []ollama.Tool{weatherTool}, Options: opts,
	})
	if err != nil {
		return nil, err
	}
	m2 := fromTiming(second.Timing)
	m.TokensIn += m2.TokensIn
	m.TokensOut += m2.TokensOut
	m.WallMs += m2.WallMs
	// Combined decode rate across both legs.
	totalEvalSec := secOf(first.EvalDuration) + secOf(second.EvalDuration)
	if totalEvalSec > 0 {
		m.DecodeTPS = float64(first.EvalCount+second.EvalCount) / totalEvalSec
	}
	return m, nil
}

func fromTiming(t ollama.Timing) *RunMetrics {
	m := &RunMetrics{
		TTFTMs:    msOf(t.LoadDuration + t.PromptEvalDuration),
		WallMs:    msOf(t.TotalDuration),
		LoadMs:    msOf(t.LoadDuration),
		TokensIn:  t.PromptEvalCount,
		TokensOut: t.EvalCount,
	}
	if s := secOf(t.PromptEvalDuration); s > 0 {
		m.PrefillTPS = float64(t.PromptEvalCount) / s
	}
	if s := secOf(t.EvalDuration); s > 0 {
		m.DecodeTPS = float64(t.EvalCount) / s
	}
	return m
}

func msOf(d time.Duration) float64  { return float64(d) / float64(time.Millisecond) }
func secOf(d time.Duration) float64 { return d.Seconds() }

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func statOf(runs []RunMetrics, get func(RunMetrics) float64) Stat {
	vals := make([]float64, len(runs))
	for i, r := range runs {
		vals[i] = get(r)
	}
	return Stat{Median: percentile(vals, 50), P95: percentile(vals, 95)}
}

// percentile uses nearest-rank on a sorted copy; report median and p95,
// never the mean.
func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	rank := int(float64(len(s))*p/100.0+0.5) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(s) {
		rank = len(s) - 1
	}
	return s[rank]
}
