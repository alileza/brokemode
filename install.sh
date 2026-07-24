#!/usr/bin/env bash
# brokemode installer — local LLMs on Apple Silicon, zero token cost.
#
#   curl -fsSL https://raw.githubusercontent.com/alileza/brokemode/main/install.sh | bash
#
# Idempotent: safe to re-run. Supports:
#   --dry-run          print what would happen, change nothing
#   --models a,b,c     pull only these models (still budget-checked)
set -euo pipefail

REPO="alileza/brokemode"
RAW_BASE="https://raw.githubusercontent.com/${REPO}/main"
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
log "detected macOS arm64 with ${MEM_GB}GB unified memory"
if [ "$MEM_GB" -lt 16 ]; then
  die "${MEM_GB}GB unified memory is below the 16GB this registry is tuned for. Edit models.yaml budgets before installing."
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
log "RSS budget: ${BUDGET_GB}GB (of ${MEM_GB}GB total)"

# ---------------------------------------------------------------- binary
BIN_DIR="$HOME/.brokemode/bin"
run mkdir -p "$BIN_DIR"

if [ -n "$REPO_DIR" ] && command -v go >/dev/null 2>&1; then
  log "building brokemode from local checkout (go build)"
  run env CGO_ENABLED=0 go -C "$REPO_DIR" build -trimpath -o "$BIN_DIR/brokemode" ./cmd/brokemode
else
  RELEASE_URL="https://github.com/${REPO}/releases/latest/download/brokemode-darwin-arm64"
  log "downloading release binary: $RELEASE_URL"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '\033[2m[dry-run]\033[0m curl -fsSL %s -o %s/brokemode\n' "$RELEASE_URL" "$BIN_DIR"
  else
    curl -fsSL "$RELEASE_URL" -o "$BIN_DIR/brokemode" \
      || die "no release binary available; clone the repo and re-run ./install.sh to build with Go"
    chmod +x "$BIN_DIR/brokemode"
  fi
fi

# ---------------------------------------------------------------- pull models
in_only_list() {
  [ -z "$ONLY_MODELS" ] && return 0
  case ",$ONLY_MODELS," in *",$1,"*) return 0 ;; *) return 1 ;; esac
}

PULLED=""
SKIPPED_BUDGET=""
while IFS='|' read -r name disk rss tps def; do
  [ "$name" = "BUDGET" ] && continue
  if [ -n "$ONLY_MODELS" ]; then
    in_only_list "$name" || continue
  else
    [ "$def" = "true" ] || continue
  fi

  over_budget="$(awk -v r="$rss" -v b="$BUDGET_GB" 'BEGIN{print (r>b) ? 1 : 0}')"
  if [ "$over_budget" -eq 1 ]; then
    warn "REFUSING to pull $name: peak RSS ${rss}GB exceeds the ${BUDGET_GB}GB budget"
    SKIPPED_BUDGET="$SKIPPED_BUDGET $name"
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
log "add this line to ~/.zshrc:"
printf '\n    source ~/.brokemode/env\n\n'

printf '%-14s %10s %12s %14s %s\n' "MODEL" "DISK(GB)" "PEAK RSS(GB)" "EXPECTED TOK/S" "STATUS"
printf '%-14s %10s %12s %14s %s\n' "-----" "--------" "------------" "--------------" "------"
while IFS='|' read -r name disk rss tps def; do
  [ "$name" = "BUDGET" ] && continue
  status="registry"
  case " $PULLED "         in *" $name "*) status="pulled" ;; esac
  case " $SKIPPED_BUDGET " in *" $name "*) status="OVER BUDGET" ;; esac
  printf '%-14s %10s %12s %14s %s\n' "$name" "$disk" "$rss" "$tps" "$status"
done < <(parse_models)
printf '\n'
log "done. Try: brokemode bench --model qwen3.5:4b"
