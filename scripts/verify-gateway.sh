#!/usr/bin/env bash
# Smoke test for the brokemode gateway: curl /v1/messages (non-streaming,
# streaming, auth) and /v1/models, print PASS/FAIL per check.
set -uo pipefail

GATEWAY="${BROKEMODE_GATEWAY:-http://127.0.0.1:9100}"
MODEL="${1:-${BROKEMODE_MODEL:-sonnet}}"
FAILED=0

check() { # name condition
  local name="$1"; shift
  if "$@" >/dev/null 2>&1; then
    printf '\033[1;32mPASS\033[0m %s\n' "$name"
  else
    printf '\033[1;31mFAIL\033[0m %s\n' "$name"
    FAILED=1
  fi
}

command -v jq >/dev/null || { echo "jq is required"; exit 1; }

echo "verifying gateway at $GATEWAY with model '$MODEL'"

# 1. /v1/models lists the registry.
MODELS_JSON="$(curl -fsS -H 'Authorization: Bearer local' "$GATEWAY/v1/models")"
check "/v1/models returns registry" \
  jq -e '.data | length > 0' <<<"$MODELS_JSON"

# 2. Unauthenticated request is rejected with 401.
STATUS="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$GATEWAY/v1/messages" \
  -H 'Content-Type: application/json' \
  -d '{"model":"'"$MODEL"'","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}')"
check "missing auth rejected (401)" test "$STATUS" = "401"

# 3. Non-streaming completion.
RESP="$(curl -fsS -X POST "$GATEWAY/v1/messages" \
  -H 'Authorization: Bearer local' -H 'Content-Type: application/json' \
  -d '{"model":"'"$MODEL"'","max_tokens":32,"messages":[{"role":"user","content":"Reply with the single word: pong"}]}')"
check "non-streaming: type=message" bash -c 'jq -e ".type == \"message\"" <<<"$1"' _ "$RESP"
check "non-streaming: content block present" bash -c 'jq -e ".content | length > 0" <<<"$1"' _ "$RESP"
check "non-streaming: usage tokens > 0" bash -c 'jq -e ".usage.input_tokens > 0 and .usage.output_tokens > 0" <<<"$1"' _ "$RESP"
check "non-streaming: stop_reason set" bash -c 'jq -e ".stop_reason != null" <<<"$1"' _ "$RESP"

# 4. Streaming completion: verify the SSE event sequence shape.
STREAM="$(curl -fsS -N -X POST "$GATEWAY/v1/messages" \
  -H 'Authorization: Bearer local' -H 'Content-Type: application/json' \
  -d '{"model":"'"$MODEL"'","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"Count to three."}]}')"
EVENTS="$(grep '^event: ' <<<"$STREAM" | sed 's/^event: //')"
check "streaming: message_start first" bash -c 'test "$(head -1 <<<"$1")" = message_start' _ "$EVENTS"
check "streaming: has content_block_start" grep -q '^content_block_start$' <<<"$EVENTS"
check "streaming: has content_block_delta" grep -q '^content_block_delta$' <<<"$EVENTS"
check "streaming: has ping" grep -q '^ping$' <<<"$EVENTS"
check "streaming: has content_block_stop" grep -q '^content_block_stop$' <<<"$EVENTS"
check "streaming: message_delta then message_stop last" \
  bash -c 'test "$(tail -2 <<<"$1" | tr "\n" " ")" = "message_delta message_stop "' _ "$EVENTS"

if [ "$FAILED" -eq 0 ]; then
  printf '\n\033[1;32mALL CHECKS PASSED\033[0m\n'
else
  printf '\n\033[1;31mSOME CHECKS FAILED\033[0m\n'
  exit 1
fi
