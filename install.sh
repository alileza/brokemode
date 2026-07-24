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
#   BROKEMODE_REPO=you/brokemode BROKEMODE_RELEASE_BASE=https://.../download
REPO="${BROKEMODE_REPO:-alileza/brokemode}"
RELEASE_BASE="${BROKEMODE_RELEASE_BASE:-https://github.com/${REPO}/releases/latest/download}"
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

ASSUME_YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=1 ;;
    --yes|-y)  ASSUME_YES=1 ;;
    --models)  ONLY_MODELS="${2:-}"; shift ;;
    --models=*) ONLY_MODELS="${1#--models=}" ;;
    *) die "unknown flag: $1 (supported: --dry-run, --yes, --models a,b,c)" ;;
  esac
  shift
done

# confirm <question>: yes on --yes; otherwise ask on the terminal — /dev/tty
# still works when the script itself arrives on stdin via curl | bash.
confirm() {
  [ "$ASSUME_YES" -eq 1 ] && return 0
  local ans=""
  if [ -t 0 ]; then
    read -r -p "$1 [Y/n] " ans
  elif [ -r /dev/tty ]; then
    read -r -p "$1 [Y/n] " ans < /dev/tty
  else
    warn "no terminal available to ask: $1 — assuming yes (pass --yes to silence this)"
    return 0
  fi
  case "$ans" in n|N|no|NO) return 1 ;; *) return 0 ;; esac
}

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

# ---------------------------------------------------------------- ollama helpers
OLLAMA_API="${OLLAMA_HOST:-http://127.0.0.1:11434}"

vercmp() { # vercmp a b -> echoes -1/0/1 comparing dotted versions
  awk -v a="${1#v}" -v b="${2#v}" 'BEGIN{
    sub(/[-+].*/,"",a); sub(/[-+].*/,"",b);
    n=split(a,x,"."); m=split(b,y,".");
    for(i=1;i<=3;i++){ xa=(i<=n)?x[i]+0:0; yb=(i<=m)?y[i]+0:0;
      if(xa<yb){print -1; exit} if(xa>yb){print 1; exit} }
    print 0 }'
}

# Both probes must never fail the script (set -e): an absent daemon or CLI
# simply yields an empty string.
client_version() { ollama --version 2>/dev/null | grep -oE '[0-9]+(\.[0-9]+)+' | head -1 || true; }
server_version() { curl -fsS --max-time 3 "$OLLAMA_API/api/version" 2>/dev/null | jq -r '.version // empty' 2>/dev/null || true; }

wait_for_server() { # wait_for_server [expected_version]
  for _ in $(seq 1 20); do
    sv="$(server_version)"
    if [ -n "$sv" ] && { [ -z "${1:-}" ] || [ "$sv" = "$1" ]; }; then
      echo "$sv"; return 0
    fi
    sleep 1
  done
  echo "${sv:-}"; return 1
}

# ensure_ollama_running: get a daemon answering on $OLLAMA_API without
# insisting on brew-managing it. Handles the launchd "Bootstrap failed: 5"
# case (stale registration after an upgrade) with a bootout + restart.
ensure_ollama_running() {
  local sv
  sv="$(server_version)"
  if [ -n "$sv" ]; then
    log "ollama daemon already answering on $OLLAMA_API (v$sv)"
    return 0
  fi
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '\033[2m[dry-run]\033[0m brew services start ollama\n'
    return 0
  fi

  log "starting ollama via brew services"
  if ! brew services start ollama 2>&1 | tee "$WORKDIR/brew-start.log"; then :; fi
  if grep -qiE 'Bootstrap failed|Failure while executing' "$WORKDIR/brew-start.log"; then
    warn "launchd refused the start (stale registration is the usual cause) — clearing it and retrying"
    /bin/launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/homebrew.mxcl.ollama.plist" 2>/dev/null || true
    if ! brew services restart ollama 2>&1 | tee -a "$WORKDIR/brew-start.log"; then :; fi
  fi

  if sv="$(wait_for_server)" && [ -n "$sv" ]; then
    log "ollama daemon is up (v$sv)"
    return 0
  fi

  # Registered as started but never answered: a wedged service. One full
  # bootout + restart cycle before giving up.
  warn "service registered but the daemon isn't answering — restarting it once"
  /bin/launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/homebrew.mxcl.ollama.plist" 2>/dev/null || true
  if ! brew services restart ollama 2>&1 | tee -a "$WORKDIR/brew-start.log"; then :; fi
  if sv="$(wait_for_server)" && [ -n "$sv" ]; then
    log "ollama daemon is up (v$sv)"
    return 0
  fi
  die "the ollama daemon never answered on $OLLAMA_API. Ways out:
    1. clear the launchd registration and retry:
         launchctl bootout gui/\$(id -u) ~/Library/LaunchAgents/homebrew.mxcl.ollama.plist
         brew services start ollama
    2. if you use the Ollama.app, open it (its daemon works fine too)
    3. or run it by hand in another terminal: ollama serve
  then re-run this installer."
}

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

ensure_ollama_running

# ---------------------------------------------------------------- binary
# One download, matched to this machine: the binary embeds the model
# registry, the web dashboard, and the bench prompt suite.
BIN_DIR="$HOME/.brokemode/bin"
run mkdir -p "$BIN_DIR"

if [ -f "$(dirname "$0")/models.yaml" ] 2>/dev/null && [ -f "$(dirname "$0")/go.mod" ]; then
  REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
else
  REPO_DIR=""
fi

if [ -n "$REPO_DIR" ] && command -v go >/dev/null 2>&1; then
  log "building brokemode from local checkout (go build)"
  run env CGO_ENABLED=0 go build -C "$REPO_DIR" -trimpath -o "$BIN_DIR/brokemode" ./cmd/brokemode
else
  OS_NAME="$(uname -s | tr '[:upper:]' '[:lower:]')"
  ARCH_NAME="$(uname -m)"
  [ "$ARCH_NAME" = "x86_64" ] && ARCH_NAME="amd64"
  ASSET="brokemode-${OS_NAME}-${ARCH_NAME}"
  log "downloading release binary: ${RELEASE_BASE}/${ASSET}"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '\033[2m[dry-run]\033[0m curl -fsSL %s/%s -o %s/brokemode\n' "$RELEASE_BASE" "$ASSET" "$BIN_DIR"
  else
    curl -fsSL "${RELEASE_BASE}/${ASSET}" -o "$BIN_DIR/brokemode" \
      || die "no release binary published yet for ${REPO} (${ASSET}). Build it yourself instead:
    git clone https://github.com/${REPO}.git && cd brokemode && ./install.sh"
    chmod +x "$BIN_DIR/brokemode"
  fi
fi

# Smoke-test the installed binary before trusting it for anything.
if [ "$DRY_RUN" -eq 0 ]; then
  if "$BIN_DIR/brokemode" --help >/dev/null 2>&1; then
    log "brokemode binary responds: $("$BIN_DIR/brokemode" --version 2>/dev/null || echo ok)"
  else
    die "installed binary failed its smoke test — try rebuilding from a clone: git clone https://github.com/${REPO}.git && cd brokemode && ./install.sh"
  fi
fi

# ---------------------------------------------------------------- models.yaml
# Registry source: local checkout file, otherwise the copy embedded in the
# binary we just installed — no second download.
if [ -n "$REPO_DIR" ]; then
  MODELS_YAML="$REPO_DIR/models.yaml"
  log "using models.yaml from local checkout: $MODELS_YAML"
elif [ "$DRY_RUN" -eq 0 ]; then
  MODELS_YAML="$WORKDIR/models.yaml"
  "$BIN_DIR/brokemode" models --export > "$MODELS_YAML" || die "could not export the embedded registry from the binary"
  log "using models.yaml embedded in the binary"
else
  # Dry-run without a checkout or binary: fall back to an existing install.
  MODELS_YAML="$HOME/.brokemode/models.yaml"
  [ -f "$MODELS_YAML" ] || die "--dry-run without a checkout needs an existing ~/.brokemode/models.yaml to preview against"
  log "using models.yaml from existing install: $MODELS_YAML"
fi

# Persist so the binary (and the user) can find/edit it from any directory.
if [ "$DRY_RUN" -eq 0 ]; then
  mkdir -p "$HOME/.brokemode"
  [ "$MODELS_YAML" = "$HOME/.brokemode/models.yaml" ] || cp "$MODELS_YAML" "$HOME/.brokemode/models.yaml"
  log "installed registry to ~/.brokemode/models.yaml"
fi

# ---------------------------------------------------------------- ollama version
# The registry states the minimum Ollama it needs. If the installed client
# is older, offer a brew upgrade; afterwards make sure the running daemon
# actually matches the client (brew upgrades the binary but the old daemon
# keeps running until restarted).
ensure_ollama_version() {
  local min="$1"
  [ -n "$min" ] || return 0
  local cv sv
  cv="$(client_version)"
  if [ -z "$cv" ]; then
    warn "could not read 'ollama --version'; skipping the version check"
    return 0
  fi

  if [ "$(vercmp "$cv" "$min")" -lt 0 ]; then
    warn "installed ollama v$cv is older than the v$min this registry requires"
    if [ "$DRY_RUN" -eq 1 ]; then
      printf '\033[2m[dry-run]\033[0m brew upgrade ollama && brew services restart ollama\n'
      return 0
    fi
    if confirm "Upgrade ollama via 'brew upgrade ollama' now?"; then
      brew upgrade ollama || die "brew upgrade ollama failed"
      cv="$(client_version)"
      if [ "$(vercmp "$cv" "$min")" -lt 0 ]; then
        die "ollama is still v$cv after the upgrade; v$min is required. Homebrew may not have a newer bottle yet — check https://github.com/ollama/ollama/releases"
      fi
      log "ollama client upgraded to v$cv"
    else
      die "ollama v$min or newer is required. Upgrade with: brew upgrade ollama && brew services restart ollama"
    fi
  else
    log "ollama client v$cv satisfies the required v$min"
  fi

  [ "$DRY_RUN" -eq 1 ] && return 0

  # Client/daemon consistency: the API must answer with the same version
  # as the CLI, otherwise requests run against a stale daemon.
  sv="$(server_version)"
  if [ -z "$sv" ]; then
    log "waiting for the ollama server to answer on $OLLAMA_API"
    sv="$(wait_for_server || true)"
  fi
  if [ "$sv" != "$cv" ]; then
    warn "ollama daemon is v${sv:-not responding} but the client is v$cv — restarting the service to match"
    brew services restart ollama >/dev/null 2>&1 || brew services start ollama >/dev/null 2>&1 || true
    sv="$(wait_for_server "$cv" || true)"
    if [ "$sv" != "$cv" ]; then
      die "ollama daemon still reports v${sv:-nothing} after restart (client v$cv). Try: brew services restart ollama, then re-run this installer."
    fi
    log "ollama daemon restarted; client and server are both v$cv"
  else
    log "ollama daemon v$sv matches the client"
  fi
}

MIN_OLLAMA="$(awk '/^min_ollama_version:/ { gsub(/"/,"",$2); print $2 }' "$MODELS_YAML")"
ensure_ollama_version "$MIN_OLLAMA"

# upgrade_and_restart_ollama: brew-upgrade the client and bounce the daemon
# so both report the same version. Returns 1 (with guidance) when brew
# can't help — e.g. Ollama came from the .app, or brew's bottle still lags.
upgrade_and_restart_ollama() {
  if ! brew upgrade ollama; then
    warn "brew upgrade ollama failed — if Ollama came from the app, download the latest from https://ollama.com/download and re-run this installer"
    return 1
  fi
  brew services restart ollama >/dev/null 2>&1 || brew services start ollama >/dev/null 2>&1 || true
  local cv sv
  cv="$(client_version)"
  sv="$(wait_for_server "$cv" || true)"
  if [ -n "$cv" ] && [ "$sv" != "$cv" ]; then
    warn "ollama daemon is v${sv:-not responding} but the client is v$cv — try: brew services restart ollama"
    return 1
  fi
  log "ollama upgraded; client and daemon are v${cv:-unknown}"
}

# pull_model <name>: ollama pull with live progress, catching the registry's
# 412 "requires a newer version of Ollama" refusal — that error is the
# authoritative version signal (newer models raise the bar ahead of any
# number models.yaml can guess), so offer the upgrade and retry once.
pull_model() {
  local name="$1" rc
  ollama pull "$name" 2>&1 | tee "$WORKDIR/pull.log"
  rc=${PIPESTATUS[0]}
  [ "$rc" -eq 0 ] && return 0
  if grep -qi "requires a newer version of Ollama" "$WORKDIR/pull.log"; then
    warn "$name needs a newer Ollama than the installed v$(client_version) (the model registry refused the pull)"
    if confirm "Upgrade ollama via 'brew upgrade ollama' and retry the pull?"; then
      if upgrade_and_restart_ollama; then
        ollama pull "$name" && return 0
        warn "still refused after the brew upgrade — Homebrew's bottle may lag behind; install the latest from https://ollama.com/download, then re-run this installer"
      fi
    else
      warn "skipping $name — upgrade Ollama and re-run this installer to pull it"
    fi
  fi
  return 1
}

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

# ---------------------------------------------------------------- pull models
in_only_list() {
  [ -z "$ONLY_MODELS" ] && return 0
  case ",$ONLY_MODELS," in *",$1,"*) return 0 ;; *) return 1 ;; esac
}

PULLED=""
SKIPPED_BUDGET=""
SKIPPED_DISK=""
SKIPPED_PULL=""
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
  elif [ "$DRY_RUN" -eq 1 ]; then
    log "pulling $name (${disk}GB on disk)"
    run ollama pull "$name"
  else
    log "pulling $name (${disk}GB on disk)"
    if ! pull_model "$name"; then
      warn "could not pull $name — continuing with the rest"
      SKIPPED_PULL="$SKIPPED_PULL $name"
      continue
    fi
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
  case " $SKIPPED_PULL "   in *" $name "*) status="PULL FAILED" ;; esac
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
