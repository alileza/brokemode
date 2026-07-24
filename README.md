# brokemode

> when you're broke and can't pay for more tokens, we gotta need a workaround

It's the 23rd of the month. The API bill has opinions. Your MacBook,
meanwhile, has a perfectly good GPU in it that's been rendering Slack.
brokemode is the workaround: run models on the machine you already own,
where the only invoice is your electricity bill and you were paying that
anyway.

Concretely: one static Go binary (`CGO_ENABLED=0`, dashboard embedded via
`embed.FS`), an Ollama backend, and a gateway that speaks the Anthropic
Messages API — so Claude Code keeps working, it just bills your M2 instead
of your card. Tuned for a MacBook Pro M2 with 16GB unified memory, i.e.
the one you have, not the one you'd buy if you weren't broke.

## Quickstart

One line, on the Mac itself:

```sh
curl -fsSL https://raw.githubusercontent.com/alileza/brokemode/main/install.sh | bash
```

To pass flags through the pipe, use `bash -s --`:

```sh
# preview without changing anything
curl -fsSL https://raw.githubusercontent.com/alileza/brokemode/main/install.sh | bash -s -- --dry-run

# pull only specific models
curl -fsSL https://raw.githubusercontent.com/alileza/brokemode/main/install.sh | bash -s -- --models qwen3.5:4b,gemma4:e4b
```

Or from a clone (builds the binary with Go instead of downloading a
release — use this if no release is published yet; the installer will
tell you):

```sh
git clone https://github.com/alileza/brokemode.git && cd brokemode && ./install.sh
```

What the installer does, in order — re-running it is always safe:

1. Refuses anything that isn't arm64 macOS; checks unified memory, CPU
   cores, and free disk, and warns (with exact GB figures) if any are short.
2. Brew-installs `ollama` and `jq` if missing; starts Ollama via
   `brew services`.
3. Downloads **one release binary matched to your machine**
   (`brokemode-darwin-arm64`) — it's fully self-contained: the model
   registry, the web dashboard, and the bench prompt suite are all
   compiled in via `embed.FS`. From a checkout it `go build`s instead.
   Either way the binary is smoke-tested before anything else happens.
4. Checks the registry's `min_ollama_version`: if the installed Ollama is
   older, it **offers to `brew upgrade ollama` on the spot** (`--yes`
   auto-accepts), then verifies the *running daemon* reports the same
   version as the CLI — brew upgrades the binary, but the stale daemon
   keeps answering until restarted, so the installer restarts the service
   when they disagree. `doctor`, `bench`, and the gateway re-check the
   server version at run time (bench refuses against an outdated server).
5. Seeds `~/.brokemode/models.yaml` from the binary's embedded registry
   (edit it there; the binary prefers the on-disk copy) and pulls every
   model marked `default: true` that fits this machine's memory budget and
   disk — anything that doesn't fit is skipped with a loud reason, never
   silently.
6. Puts `brokemode` **on your PATH immediately** (a symlink into brew's
   bin, so it works in the same terminal) and wires
   `source ~/.brokemode/env` into your shell rc idempotently — new
   terminals get the gateway variables automatically; for the current one,
   `source ~/.brokemode/env`. Then it prints a summary table (fit
   verdicts, recommended + fastest picks) and numbered next steps.
   The env file points Claude Code at the local gateway by default —
   values you export yourself win, and commenting the two `ANTHROPIC_*`
   lines out of `~/.brokemode/env` keeps Claude Code on the real API.

Then:

```sh
source ~/.brokemode/env
brokemode doctor                # what can THIS machine run? warnings + picks
brokemode bench                 # 3 warmup + 5 measured runs per prompt, median/p95
brokemode serve                 # gateway :9100 + dashboard/metrics :9101
brokemode tui                   # terminal dashboard, 1Hz
brokemode models                # registry + budget verdicts
```

`brokemode doctor` (and the installer) inspect the actual machine — unified
memory, cores, free disk, live memory pressure, thermal state — and classify
every registry model as **comfortable / tight / no-fit** against an
effective budget of `min(max_rss_gb, memory − 5GB reserved for macOS)`. You
get a recommended model (best quality that fits comfortably), a fast-lane
pick (highest tok/s), and loud warnings when memory is short, when disk
needs freeing (with the exact GB figure), or when the machine is already
under pressure or throttled:

```
machine: 16GB unified memory, 8 cores, 500.0GB free disk
effective RSS budget: 11.0GB

MODEL          FIT          WHY
qwen3.5:9b     comfortable  7.8GB RSS leaves 3.2GB headroom  ← recommended
gemma4:e4b     comfortable  3.4GB RSS leaves 7.6GB headroom  ← fastest
gemma4:12b     tight        10.2GB RSS against a 11.0GB budget — close everything else first
```

From a checkout: `make build` (Vite build, then `go build` with the
dashboard embedded — node is a build-time dependency only).

## The memory budget

Being broke is a discipline, and the discipline here is memory. 16GB
sounds like a lot until macOS takes its cut. `models.yaml` carries a
global `max_rss_gb: 11` budget, and both `install.sh` and every
CLI/gateway load path refuse to pull or serve a model whose measured peak
RSS exceeds it. 11GB leaves ~5GB for macOS itself; past that the
compressor starts trading your decode rate for survival, and now you're
broke *and* slow.

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

## Claude Code, but it bills your Mac

This is the whole point of the operation. `brokemode gateway` implements
the Anthropic Messages API surface Claude Code needs — streaming SSE with
the exact event sequence, tool_use/tool_result translation, stop_reason
mapping, usage accounting — backed by Ollama, with per-request
TTFT/decode/RSS instrumentation (that's why it's ours and not an
off-the-shelf proxy). Claude Code cannot tell it's been handed a budget
airline seat; it boards normally.

```sh
brokemode serve        # gateway on :9100, dashboard on :9101
export ANTHROPIC_BASE_URL="http://127.0.0.1:9100"
export ANTHROPIC_AUTH_TOKEN="brokemode-local"   # any non-empty token
claude                 # Claude Code now talks to qwen3.5 on your Mac
```

Model aliases come from `models.yaml`: requests for `claude-sonnet-5` —
including dated variants and Claude Code's bracketed flavors like
`claude-opus-5[1m]` — hit `qwen3.5:9b`, `claude-haiku-4-5` hits
`qwen3.5:4b`, each with its registry `num_ctx` applied. The env file also
presets `ANTHROPIC_MODEL`/`ANTHROPIC_SMALL_FAST_MODEL` so a fresh Claude
Code session starts on models the gateway serves; if it ever complains
about a selected model, run `/model` inside Claude Code and pick
`claude-sonnet-5`. `GET /v1/models` serves the registry for model
discovery. Verify a running gateway with `./scripts/verify-gateway.sh`.

Temper expectations: a 9B Q4 model is not Sonnet, and nobody here will
pretend it is. It is, however, free, private, always awake, and incapable
of emailing you about usage tiers.

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

Champagne models on a beer machine — knowing what you can't afford is half
of being broke competently. Unified memory is shared: the OS,
WindowServer, and your browser hold ~4–5GB before Ollama loads anything.
The practical model budget is ~11GB — hence `max_rss_gb`.

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

## Troubleshooting

- **"no release binary published yet"** — the repo has no GitHub release
  carrying a `brokemode-<os>-<arch>` asset for your machine yet. Clone and
  run `./install.sh` instead; it builds with Go (`brew install go` if
  needed).
- **"is ollama running?"** on any command — `brew services start ollama`,
  then `ollama list` to confirm the daemon answers.
- **"ollama server vX is older than the required vY"** — the registry needs
  a newer Ollama. Re-run `./install.sh` (it offers the upgrade and restarts
  the daemon), or manually: `brew upgrade ollama && brew services restart
  ollama`. The daemon restart matters: an upgraded CLI with a stale daemon
  is exactly the inconsistency this check exists to catch.
- **`Error: pull model manifest: 412` / "requires a newer version of
  Ollama"** — the model registry itself refused the pull; newer models
  raise the version bar ahead of anything models.yaml can predict. The
  installer catches this, offers the upgrade, restarts the daemon, and
  retries the pull. If it still fails after the upgrade, Homebrew's bottle
  is lagging — install the latest from
  [ollama.com/download](https://ollama.com/download) and re-run. (Same if
  Ollama came from the .app instead of brew: `brew upgrade` can't touch it.)
- **GPU/power columns empty, run marked PARTIAL** — `powermetrics` needs
  root. Run `sudo -v` right before `brokemode bench`, or add a NOPASSWD
  sudoers rule for `/usr/bin/powermetrics`. Everything else still works.
- **Registry confusion** — the binary looks for models.yaml in the current
  directory, next to the binary, then `~/.brokemode/models.yaml`, and
  finally falls back to the copy embedded at build time (so a bare binary
  always runs). `brokemode models` prints which source it resolved;
  `brokemode models --export` dumps the raw YAML; `--models-yaml path`
  overrides everything.
- **"NOT ENOUGH DISK" / "REFUSING to pull"** — the message includes the
  exact GB to free or the machine size the model needs. `brokemode doctor`
  re-checks after you clean up. Ollama blobs live in `~/.ollama/models`;
  `ollama rm <model>` reclaims space.
- **Everything is slow and the fans are on** — check `brokemode tui`: if
  thermal shows THROTTLED or memory pressure is red, close apps or let the
  machine cool; bench results during throttle are marked with a warning.

**Uninstall:** `rm -rf ~/.brokemode`, remove the `source ~/.brokemode/env`
line from `~/.zshrc`, and optionally `brew services stop ollama` /
`brew uninstall ollama` (model blobs are in `~/.ollama`).

## Development

```sh
make build     # vite build + static go build
make test      # go tests (gateway SSE + tool round trip run against a fake Ollama)
make lint      # golangci-lint + eslint + prettier
make verify    # curl smoke test against a running gateway
```

**Releasing:** versions bump themselves. Every code push to `main` cuts a
**patch** release automatically (docs/markdown/workflow-only changes
don't); to bump **minor** or **major**, run the `release` workflow from
the Actions tab and pick the bump size — it defaults to patch. The
workflow computes the next semver from the latest `v*` tag, builds the
dashboard, runs the tests, then publishes
`brokemode-{darwin,linux}-{arm64,amd64}` binaries plus `checksums.txt` to
a GitHub release — each one self-contained, which is what lets install.sh
download exactly one file. A commit that's already tagged is skipped, so
re-runs are safe. CI (`.github/workflows/ci.yml`) runs lint, tests, and a
darwin/arm64 cross-compile on every push and PR.

Working on a fork? The installer honors `BROKEMODE_REPO` and
`BROKEMODE_RELEASE_BASE` overrides, which is also how its download path is
integration-tested against a local server.

No Docker, no cloud dependencies, no Python. Shell is allowed only in
`install.sh`. MIT licensed.
