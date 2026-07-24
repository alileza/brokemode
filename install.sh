#!/usr/bin/env bash
# brokemode installer — local LLMs on Apple Silicon, zero token cost.
#
#   curl -fsSL https://raw.githubusercontent.com/alileza/brokemode/main/install.sh | bash
#
# Idempotent: safe to re-run. Supports:
#   --dry-run          print what would happen, change nothing
#   --models a,b,c     pull only these models (still budget-checked)
set -euo pipefail

# Overridable for forks and testing:
#   BROKEMODE_REPO=you/brokemode BROKEMODE_RAW_BASE=... BROKEMODE_RELEASE_URL=...
REPO="${BROKEMODE_REPO:-alileza/brokemode}"
RAW_BASE="${BROKEMODE_RAW_BASE:-https://raw.githubusercontent.com/${REPO}/main}"
RELEASE_URL="${BROKEMODE_RELEASE_URL:-https://github.com/${REPO}/releases/latest/download/brokemode-darwin-arm64}"
BREW_PREFIX=""
DRY_RUN=0
ONLY_MODELS=""

log()  { printf '\033[1;36m[brokemode]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[brokemode] WARN:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[brokemode] ABORT:\033[0m %s\n' "$*" >&2; exit 1; }
run()  {
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '\033[2m[dry-run]\033[0m %s\n' "$*"
  else
    "$@"
  fi
}

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=1 ;;
    --models)  ONLY_MODELS="${2:-}"; shift ;;
    --models=*) ONLY_MODELS="${1#--models=}" ;;
    *) die "unknown flag: $1 (supported: --dry-run, --models a,b,c)" ;;
  esac
  shift
done

# ---------------------------------------------------------------- platform
[ "$(uname -s)" = "Darwin" ] || die "brokemode targets macOS on Apple Silicon; detected $(uname -s). Nothing was changed."
[ "$(uname -m)" = "arm64" ]  || die "brokemode requires an Apple Silicon (arm64) Mac; detected $(uname -m). Rosetta shells also trigger this — re-run from a native arm64 terminal."

MEM_BYTES="$(sysctl -n hw.memsize)"
MEM_GB=$(( MEM_BYTES / 1024 / 1024 / 1024 ))
CPU_CORES="$(sysctl -n hw.ncpu 2>/dev/null || echo 0)"
DISK_FREE_GB="$(df -Pk "$HOME" | awk 'NR==2 {printf "%.1f", $4/1024/1024}')"
log "detected macOS arm64: ${MEM_GB}GB unified memory, ${CPU_CORES} cores, ${DISK_FREE_GB}GB free disk"

if [ "$MEM_GB" -lt 8 ]; then
  die "${MEM_GB}GB unified memory cannot run any model in this registry. Nothing was changed."
fi
if [ "$MEM_GB" -lt 16 ]; then
  warn "${MEM_GB}GB unified memory is below the 16GB this registry is tuned for — the budget will be tightened and only the smallest models will fit. Close other apps before running anything."
fi
if [ "$CPU_CORES" -gt 0 ] && [ "$CPU_CORES" -lt 8 ]; then
  warn "${CPU_CORES} CPU cores — prefill on long prompts will be slower than the registry's expected rates."
fi

# ---------------------------------------------------------------- homebrew deps
command -v brew >/dev/null 2>&1 || die "Homebrew is required: https://brew.sh"
BREW_PREFIX="$(brew --prefix)"

for pkg in ollama jq; do
  if brew list --formula "$pkg" >/dev/null 2>&1 || command -v "$pkg" >/dev/null 2>&1; then
    log "$pkg already installed"
  else
    log "installing $pkg via brew"
    run brew install "$pkg"
  fi
done

if brew services list 2>/dev/null | grep -E '^ollama\s+started' >/dev/null; then
  log "ollama service already running"
else
  log "starting ollama via brew services"
  run brew services start ollama
fi

# ---------------------------------------------------------------- models.yaml
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

if [ -f "$(dirname "$0")/models.yaml" ] 2>/dev/null && [ -f "$(dirname "$0")/go.mod" ]; then
  REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
  MODELS_YAML="$REPO_DIR/models.yaml"
  log "using models.yaml from local checkout: $MODELS_YAML"
else
  REPO_DIR=""
  MODELS_YAML="$WORKDIR/models.yaml"
  log "fetching models.yaml from ${RAW_BASE}"
  curl -fsSL "${RAW_BASE}/models.yaml" -o "$MODELS_YAML" || die "could not fetch models.yaml"
fi

# Parse our (fixed-shape) models.yaml with awk into pipe-separated rows:
#   name|disk_gb|peak_rss_gb|expected_tps|default
parse_models() {
  awk '
    /^max_rss_gb:/ { print "BUDGET|" $2; next }
    /^  - name:/   { if (name != "") emit(); name=$3; disk=""; rss=""; tps=""; def="false"; next }
    /^    disk_gb:/      { disk=$2; next }
    /^    peak_rss_gb:/  { rss=$2;  next }
    /^    expected_tps:/ { tps=$2;  next }
    /^    default: true/ { def="true"; next }
    END { if (name != "") emit() }
    function emit() { print name "|" disk "|" rss "|" tps "|" def }
  ' "$MODELS_YAML"
}

BUDGET_GB="$(parse_models | awk -F'|' '$1=="BUDGET"{print $2}')"
[ -n "$BUDGET_GB" ] || die "models.yaml is missing max_rss_gb"

# Effective budget: min(max_rss_gb, memory - 5GB reserved for macOS), so a
# smaller machine automatically tightens the registry's tuned budget.
EFFECTIVE_BUDGET_GB="$(awk -v b="$BUDGET_GB" -v m="$MEM_GB" 'BEGIN{u=m-5; if(u<0)u=0; print (u<b)?u:b}')"
log "RSS budget: ${EFFECTIVE_BUDGET_GB}GB effective (max_rss_gb=${BUDGET_GB}, ${MEM_GB}GB memory - 5GB reserved for macOS)"

# fit_of <peak_rss>: comfortable / tight / no-fit against the effective budget.
fit_of() {
  awk -v r="$1" -v b="$EFFECTIVE_BUDGET_GB" 'BEGIN{
    if (r <= b - 1.5)      print "comfortable";
    else if (r <= b)       print "tight";
    else                   print "no-fit";
  }'
}

# The recommended model: heaviest comfortable fit (peak RSS tracks quality)
# with enough free disk; fastest pick: highest expected tok/s that fits.
RECOMMENDED=""
FASTEST=""
best_rss=0
best_tps=0
while IFS='|' read -r name disk rss tps def; do
  [ "$name" = "BUDGET" ] && continue
  [ "$(fit_of "$rss")" = "comfortable" ] || continue
  disk_ok="$(awk -v f="$DISK_FREE_GB" -v d="$disk" 'BEGIN{print (f >= d+2) ? 1 : 0}')"
  [ "$disk_ok" -eq 1 ] || continue
  if awk -v a="$rss" -v b="$best_rss" 'BEGIN{exit !(a>b)}'; then best_rss="$rss"; RECOMMENDED="$name"; fi
  if awk -v a="$tps" -v b="$best_tps" 'BEGIN{exit !(a>b)}'; then best_tps="$tps"; FASTEST="$name"; fi
done < <(parse_models)
if [ -z "$RECOMMENDED" ]; then
  warn "no registry model fits this machine comfortably (memory or disk) — see the summary table below for what to free up."
fi

# ---------------------------------------------------------------- binary
BIN_DIR="$HOME/.brokemode/bin"
run mkdir -p "$BIN_DIR"

if [ -n "$REPO_DIR" ] && command -v go >/dev/null 2>&1; then
  log "building brokemode from local checkout (go build)"
  run env CGO_ENABLED=0 go build -C "$REPO_DIR" -trimpath -o "$BIN_DIR/brokemode" ./cmd/brokemode
else
  log "downloading release binary: $RELEASE_URL"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '\033[2m[dry-run]\033[0m curl -fsSL %s -o %s/brokemode\n' "$RELEASE_URL" "$BIN_DIR"
  else
    curl -fsSL "$RELEASE_URL" -o "$BIN_DIR/brokemode" \
      || die "no release binary published yet for ${REPO}. Build it yourself instead:
    git clone https://github.com/${REPO}.git && cd brokemode && ./install.sh"
    chmod +x "$BIN_DIR/brokemode"
  fi
fi

# The binary resolves models.yaml from CWD, its own directory, or
# ~/.brokemode — persist the registry so a pipe install works from any
# directory, not just the (deleted) temp dir we fetched it into.
if [ "$DRY_RUN" -eq 0 ]; then
  mkdir -p "$HOME/.brokemode"
  cp "$MODELS_YAML" "$HOME/.brokemode/models.yaml"
  log "installed registry to ~/.brokemode/models.yaml"
fi

# Smoke-test the installed binary (skip in dry-run and when it was
# cross-compiled for a different OS during testing).
if [ "$DRY_RUN" -eq 0 ] && [ -x "$BIN_DIR/brokemode" ]; then
  if "$BIN_DIR/brokemode" --help >/dev/null 2>&1; then
    log "brokemode binary responds: $("$BIN_DIR/brokemode" --help 2>/dev/null | head -1)"
  else
    warn "installed binary failed its smoke test — try rebuilding from a clone: git clone https://github.com/${REPO}.git && cd brokemode && ./install.sh"
  fi
fi

# ---------------------------------------------------------------- pull models
in_only_list() {
  [ -z "$ONLY_MODELS" ] && return 0
  case ",$ONLY_MODELS," in *",$1,"*) return 0 ;; *) return 1 ;; esac
}

PULLED=""
SKIPPED_BUDGET=""
SKIPPED_DISK=""
while IFS='|' read -r name disk rss tps def; do
  [ "$name" = "BUDGET" ] && continue
  if [ -n "$ONLY_MODELS" ]; then
    in_only_list "$name" || continue
  else
    [ "$def" = "true" ] || continue
  fi

  over_budget="$(awk -v r="$rss" -v b="$EFFECTIVE_BUDGET_GB" 'BEGIN{print (r>b) ? 1 : 0}')"
  if [ "$over_budget" -eq 1 ]; then
    needed_mem="$(awk -v r="$rss" 'BEGIN{printf "%.0f", r+5}')"
    warn "REFUSING to pull $name: peak RSS ${rss}GB exceeds the ${EFFECTIVE_BUDGET_GB}GB budget — this model needs a ~${needed_mem}GB machine"
    SKIPPED_BUDGET="$SKIPPED_BUDGET $name"
    continue
  fi

  disk_short="$(awk -v f="$DISK_FREE_GB" -v d="$disk" 'BEGIN{s=d+2-f; if(s>0) printf "%.1f", s; else print 0}')"
  if [ "$disk_short" != "0" ]; then
    warn "NOT ENOUGH DISK for $name: needs ${disk}GB + 2GB headroom, only ${DISK_FREE_GB}GB free — free up at least ${disk_short}GB and re-run"
    SKIPPED_DISK="$SKIPPED_DISK $name"
    continue
  fi

  if [ "$DRY_RUN" -eq 0 ] && ollama list 2>/dev/null | awk '{print $1}' | grep -qx "$name"; then
    log "$name already pulled"
  else
    log "pulling $name (${disk}GB on disk)"
    run ollama pull "$name"
  fi
  PULLED="$PULLED $name"
done < <(parse_models)

[ -n "$PULLED" ] || warn "no models pulled (check --models spelling against models.yaml)"

# ---------------------------------------------------------------- env file
ENV_FILE="$HOME/.brokemode/env"
log "writing $ENV_FILE"
if [ "$DRY_RUN" -eq 0 ]; then
  mkdir -p "$HOME/.brokemode"
  cat > "$ENV_FILE" <<EOF
# brokemode environment — source this from ~/.zshrc
export PATH="\$HOME/.brokemode/bin:\$PATH"
export OLLAMA_HOST="http://127.0.0.1:11434"
export BROKEMODE_GATEWAY="http://127.0.0.1:9100"
# Point Claude Code at the local gateway (any non-empty token works):
export ANTHROPIC_BASE_URL="http://127.0.0.1:9100"
export ANTHROPIC_AUTH_TOKEN="brokemode-local"
EOF
fi

# ---------------------------------------------------------------- summary
printf '\n'

printf '%-14s %10s %12s %14s %-12s %s\n' "MODEL" "DISK(GB)" "PEAK RSS(GB)" "EXPECTED TOK/S" "FIT" "STATUS"
printf '%-14s %10s %12s %14s %-12s %s\n' "-----" "--------" "------------" "--------------" "---" "------"
while IFS='|' read -r name disk rss tps def; do
  [ "$name" = "BUDGET" ] && continue
  status="registry"
  case " $PULLED "         in *" $name "*) status="pulled" ;; esac
  case " $SKIPPED_BUDGET " in *" $name "*) status="OVER BUDGET" ;; esac
  case " $SKIPPED_DISK "   in *" $name "*) status="NEEDS DISK" ;; esac
  fit="$(fit_of "$rss")"
  marker=""
  [ "$name" = "$FASTEST" ] && marker="  <- fastest"
  [ "$name" = "$RECOMMENDED" ] && marker="  <- recommended"
  printf '%-14s %10s %12s %14s %-12s %s%s\n' "$name" "$disk" "$rss" "$tps" "$fit" "$status" "$marker"
done < <(parse_models)
printf '\n'
if [ -n "$RECOMMENDED" ]; then
  log "recommended for this machine: $RECOMMENDED"
  [ -n "$FASTEST" ] && [ "$FASTEST" != "$RECOMMENDED" ] && log "fast lane: $FASTEST"
else
  warn "nothing fits comfortably yet — free up memory/disk (see warnings above), then run 'brokemode doctor'."
fi

BENCH_MODEL="${RECOMMENDED:-<model>}"
cat <<EOF

──────────────────────────────────────────────────────────────
 NEXT STEPS
──────────────────────────────────────────────────────────────
 1. Enable the environment (adds PATH + gateway vars, once):

      grep -q 'brokemode/env' ~/.zshrc || echo 'source ~/.brokemode/env' >> ~/.zshrc
      source ~/.brokemode/env

 2. Check what this machine can run:

      brokemode doctor

 3. Benchmark the recommended model (fills results/summary.md):

      brokemode bench --model ${BENCH_MODEL}

 4. Start the gateway + dashboard, then point Claude Code at it:

      brokemode serve          # gateway :9100, dashboard http://127.0.0.1:9101

      # in another terminal (already set if you sourced ~/.brokemode/env):
      export ANTHROPIC_BASE_URL=http://127.0.0.1:9100
      export ANTHROPIC_AUTH_TOKEN=brokemode-local
      claude

 Re-running this installer is always safe (idempotent).
 Uninstall: rm -rf ~/.brokemode && brew services stop ollama
──────────────────────────────────────────────────────────────
EOF
