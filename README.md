# brokemode

Run and benchmark local LLMs on Apple Silicon — the premise being **zero
token cost**. Tuned for a MacBook Pro M2 with 16GB unified memory.

One static Go binary (`CGO_ENABLED=0`, dashboard embedded via `embed.FS`),
an Ollama backend, and a gateway that speaks the Anthropic Messages API so
Claude Code runs against your own silicon.

## Quickstart

```sh
curl -fsSL https://raw.githubusercontent.com/alileza/brokemode/main/install.sh | bash
```

The installer refuses to run on anything but arm64 macOS, brew-installs
`ollama` + `jq` if missing, starts Ollama via `brew services`, pulls every
model marked `default: true` in [models.yaml](models.yaml), and writes
`~/.brokemode/env`. Re-running is safe. `--dry-run` previews, `--models
qwen3.5:4b,gemma4:e4b` narrows the pull set.

Then:

```sh
source ~/.brokemode/env
brokemode bench                 # 3 warmup + 5 measured runs per prompt, median/p95
brokemode serve                 # gateway :9100 + dashboard/metrics :9101
brokemode tui                   # terminal dashboard, 1Hz
brokemode models                # registry + budget verdicts
```

From a checkout: `make build` (Vite build, then `go build` with the
dashboard embedded — node is a build-time dependency only).

## The memory budget

`models.yaml` carries a global `max_rss_gb: 11` budget. Both `install.sh`
and every CLI/gateway load path refuse to pull or serve a model whose
measured peak RSS exceeds it. 11GB leaves ~5GB for macOS itself; past that
the compressor starts trading your decode rate for survival.

| model | quant | disk | peak RSS | num_ctx | expected tok/s | use when |
|---|---|---|---|---|---|---|
| qwen3.5:9b | Q4_K_M | 5.9GB | 7.8GB | 16384 | ~19 | daily driver, code review, agentic tool-calling |
| qwen3.5:4b | Q4_K_M | 2.6GB | 4.1GB | 32768 | ~42 | fast lane: quick chat, summarization |
| gemma4:12b | Q4_K_M | 7.6GB | 10.2GB | 8192 | ~13 | max quality, single-task, close your browser |
| gemma4:e4b | Q4_0 | 3.1GB | 3.4GB | 32768 | ~55 | cheapest RSS, coexists with a heavy IDE |

## Benchmarks

`brokemode bench` drives Ollama's `/api/generate` with `stream:false` and
derives everything from the native timing fields (`load_duration`,
`prompt_eval_*`, `eval_*`): TTFT, prefill tok/s, decode tok/s, wall time.
Five prompt classes (short chat, 1k summarization, 4k code review, JSON
structured output, tool-call round trip), 3 warmup + 5 measured runs,
**median and p95 — never the mean**.

While a case runs, host telemetry is sampled at 1Hz: GPU residency and
package power (`sudo powermetrics`), memory pressure, wired/compressed
pages, the Ollama runner's RSS, and thermal state. Without sudo the run is
loudly marked **partial** — never silently skipped. If the last measured
run decodes >15% slower than the first, you get a thermal-throttle warning.

Output lands in `results/<model>-<timestamp>.json` plus a regenerated
`results/summary.md`. Run `brokemode bench` on your own machine to fill
this table:

| model | case | TTFT ms (med) | decode tok/s (med/p95) |
|---|---|---|---|
| _run `brokemode bench`_ | | | |

## Claude Code on your own silicon

`brokemode gateway` implements the Anthropic Messages API surface Claude
Code needs — streaming SSE with the exact event sequence, tool_use/
tool_result translation, stop_reason mapping, usage accounting — backed by
Ollama, with per-request TTFT/decode/RSS instrumentation (that's why it's
ours and not an off-the-shelf proxy).

```sh
brokemode serve        # gateway on :9100, dashboard on :9101
export ANTHROPIC_BASE_URL="http://127.0.0.1:9100"
export ANTHROPIC_AUTH_TOKEN="brokemode-local"   # any non-empty token
claude                 # Claude Code now talks to qwen3.5 on your Mac
```

Model aliases come from `models.yaml`: requests for `claude-sonnet-5` (or
dated variants) hit `qwen3.5:9b`, `claude-haiku-4-5` hits `qwen3.5:4b`,
each with its registry `num_ctx` applied. `GET /v1/models` serves the
registry for Claude Code's model discovery. Verify a running gateway with
`./scripts/verify-gateway.sh`.

Temper expectations: a 9B Q4 model is not Sonnet. It is, however, free,
private, and yours.

## Monitoring

- `brokemode tui` — live tok/s, RSS vs the 11GB budget, GPU residency,
  memory pressure, thermal state.
- `brokemode serve` — Prometheus `/metrics`
  (`brokemode_decode_tokens_per_second`, `brokemode_ttft_seconds`,
  `brokemode_resident_bytes`, `mac_gpu_active_ratio`,
  `mac_memory_pressure_percent`, `mac_thermal_level`), the embedded web
  dashboard at `/`, and a 1Hz SSE telemetry feed at `/api/stream`.
- `monitor/grafana-dashboard.json` imports into Grafana (Cloud or local);
  `monitor/prometheus.yml` scrapes :9101.

## What does NOT fit on 16GB, and why

Unified memory is shared: the OS, WindowServer, and your browser hold
~4–5GB before Ollama loads anything. The practical model budget is ~11GB —
hence `max_rss_gb`.

- **14B+ at Q4** (qwen3.5:14b ≈ 11–12GB peak RSS): loads, then the first
  4k-token prefill pushes wired memory past the compressor's tipping
  point. Decode drops from ~11 tok/s to low single digits while
  `memory_pressure` pegs. Technically runs; practically unusable.
- **Any 30B/32B, even at Q3**: weights alone exceed the budget before KV
  cache. macOS will kill Ollama or swap it to death.
- **Long contexts on 12B**: gemma4:12b at 10.2GB RSS fits only with
  `num_ctx: 8192`. KV cache grows linearly with context — 32k context on a
  12B costs multiple extra GB you do not have. That's why each registry
  entry pins its own `num_ctx`.
- **Two models resident at once** (e.g. 9B + 12B): Ollama keeps both
  runners alive; combined RSS blows the budget on the second load. Set
  `OLLAMA_MAX_LOADED_MODELS=1` if you switch models often.

Rule of thumb on 16GB: one model ≤ Q4 10GB RSS, context sized to leave
headroom, and watch the compressed-pages line in the TUI.

## Development

```sh
make build     # vite build + static go build
make test      # go tests (gateway SSE + tool round trip run against a fake Ollama)
make lint      # golangci-lint + eslint + prettier
make verify    # curl smoke test against a running gateway
```

No Docker, no cloud dependencies, no Python. Shell is allowed only in
`install.sh`. MIT licensed.
