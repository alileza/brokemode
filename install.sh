#!/usr/bin/env bash
# brokemode installer — local LLMs on Apple Silicon, zero token cost.
#
#   curl -fsSL https://raw.githubusercontent.com/alileza/brokemode/main/install.sh | bash
#
# Gets the brokemode binary installed and on your PATH (plus a running,
# version-matched Ollama). Everything after that — model pulls, doctor,
# launching Claude Code — lives in the binary itself: just run `brokemode`.
#
# Idempotent: safe to re-run. Supports:
#   --dry-run   print what would happen, change nothing
#   --yes, -y   answer yes to every prompt
set -euo pipefail

# Overridable for forks and testing:
#   BROKEMODE_REPO=you/brokemode BROKEMODE_RELEASE_BASE=https://.../download
REPO="${BROKEMODE_REPO:-alileza/brokemode}"
RELEASE_BASE="${BROKEMODE_RELEASE_BASE:-https://github.com/${REPO}/releases/latest/download}"
BREW_PREFIX=""
DRY_RUN=0

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
    *) die "unknown flag: $1 (supported: --dry-run, --yes)" ;;
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

# ---------------------------------------------------------------- env + PATH
ENV_FILE="$HOME/.brokemode/env"
log "writing $ENV_FILE"
if [ "$DRY_RUN" -eq 0 ]; then
  mkdir -p "$HOME/.brokemode"
  cat > "$ENV_FILE" <<EOF
# brokemode environment (sourced from your shell rc by install.sh)
export PATH="\$HOME/.brokemode/bin:\$PATH"
export OLLAMA_HOST="\${OLLAMA_HOST:-http://127.0.0.1:11434}"
export BROKEMODE_GATEWAY="\${BROKEMODE_GATEWAY:-http://127.0.0.1:9100}"
# Point Claude Code at the local gateway (any non-empty token works).
# Values you export yourself win; comment these out to keep Claude Code
# on the real API by default.
export ANTHROPIC_BASE_URL="\${ANTHROPIC_BASE_URL:-http://127.0.0.1:9100}"
export ANTHROPIC_AUTH_TOKEN="\${ANTHROPIC_AUTH_TOKEN:-brokemode-local}"
# Start Claude Code on models this gateway actually serves (aliases from
# models.yaml) instead of whatever it picked last:
export ANTHROPIC_MODEL="\${ANTHROPIC_MODEL:-claude-sonnet-5}"
export ANTHROPIC_SMALL_FAST_MODEL="\${ANTHROPIC_SMALL_FAST_MODEL:-claude-haiku-4-5}"
EOF
fi

# On PATH immediately: brew's bin is already on every macOS PATH, so a
# symlink there beats waiting for a shell restart.
if [ -d "$BREW_PREFIX/bin" ]; then
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '\033[2m[dry-run]\033[0m ln -sf %s/brokemode %s/bin/brokemode\n' "$BIN_DIR" "$BREW_PREFIX"
  elif ln -sf "$BIN_DIR/brokemode" "$BREW_PREFIX/bin/brokemode" 2>/dev/null; then
    log "linked $BREW_PREFIX/bin/brokemode — 'brokemode' works in this terminal right now"
  else
    warn "could not link into $BREW_PREFIX/bin — 'brokemode' is on PATH after you restart your shell"
  fi
fi

# Wire the env file into the shell rc (idempotent) so new terminals get
# the gateway variables too.
case "$(basename "${SHELL:-/bin/zsh}")" in
  zsh)  RC_FILE="$HOME/.zshrc" ;;
  bash) RC_FILE="$HOME/.bash_profile" ;;
  *)    RC_FILE="" ;;
esac
if [ -n "$RC_FILE" ]; then
  if grep -qs 'brokemode/env' "$RC_FILE"; then
    log "$(basename "$RC_FILE") already sources ~/.brokemode/env"
  elif [ "$DRY_RUN" -eq 1 ]; then
    printf '\033[2m[dry-run]\033[0m echo '"'"'source ~/.brokemode/env'"'"' >> %s\n' "$RC_FILE"
  else
    printf '\n# brokemode\nsource "$HOME/.brokemode/env"\n' >> "$RC_FILE"
    log "added 'source ~/.brokemode/env' to $(basename "$RC_FILE")"
  fi
else
  warn "unrecognized shell '$(basename "${SHELL:-}")' — add this line to its rc file yourself: source ~/.brokemode/env"
fi

# ---------------------------------------------------------------- done
cat <<EOF

──────────────────────────────────────────────────────────────
 brokemode is installed and on your PATH.

 Just run:

      brokemode

 It shows how Claude model names map onto local models, pulls
 what fits this machine, and launches Claude Code with every
 env var preconfigured. Other doors into the same house:

      brokemode doctor    what can this machine run?
      brokemode pull      download the recommended model
      brokemode serve     gateway :9100 + dashboard :9101
      brokemode update    self-update the binary

 (Gateway vars land in new terminals via ~/.brokemode/env; for
 this one: source ~/.brokemode/env)

 Re-running this installer is always safe (idempotent).
 Uninstall: rm -rf ~/.brokemode, remove the brokemode lines from
 your shell rc, rm \$(brew --prefix)/bin/brokemode, and optionally
 brew services stop ollama
──────────────────────────────────────────────────────────────
EOF
