#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${PORT:-18080}"
RATE_LIMIT="${RATE_LIMIT:-2}"
RUN_UNIT_TESTS=1
RUN_UPSTREAM_TEST=1

ACTIVE_KEY="codex-active-test-key"
INACTIVE_KEY="codex-inactive-test-key"
RATE_KEY="codex-rate-test-key"

SERVER_PID=""
TMP_DIR=""

usage() {
  cat <<'EOF'
Usage: ./scripts/test_gateway.sh [options]

Options:
  --port <port>          Port to run the local gateway on. Default: 18080
  --rate-limit <count>   Requests allowed per minute for the rate-limit test. Default: 2
  --skip-unit            Skip running go test ./...
  --skip-upstream        Skip the upstream proxy check
  --help                 Show this help message
EOF
}

log() {
  printf '[test] %s\n' "$*"
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

cleanup() {
  if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    kill "$SERVER_PID" >/dev/null 2>&1 || true
    wait "$SERVER_PID" >/dev/null 2>&1 || true
  fi
  if [[ -n "$TMP_DIR" ]] && [[ -d "$TMP_DIR" ]]; then
    rm -rf "$TMP_DIR"
  fi
}

trap cleanup EXIT

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --port)
        [[ $# -ge 2 ]] || fail "--port requires a value"
        PORT="$2"
        shift 2
        ;;
      --rate-limit)
        [[ $# -ge 2 ]] || fail "--rate-limit requires a value"
        RATE_LIMIT="$2"
        shift 2
        ;;
      --skip-unit)
        RUN_UNIT_TESTS=0
        shift
        ;;
      --skip-upstream)
        RUN_UPSTREAM_TEST=0
        shift
        ;;
      --help)
        usage
        exit 0
        ;;
      *)
        fail "unknown argument: $1"
        ;;
    esac
  done
}

load_env() {
  if [[ -f "$ROOT_DIR/.env" ]]; then
    set -a
    # shellcheck disable=SC1091
    source "$ROOT_DIR/.env"
    set +a
  fi

  : "${DATABASE_URL:?DATABASE_URL must be set in the environment or .env}"
  OPENROUTER_API_KEY="${OPENROUTER_API_KEY:-invalid-openrouter-key-for-smoke-tests}"
  export DATABASE_URL OPENROUTER_API_KEY
}

hash_key() {
  printf '%s' "$1" | sha256sum | awk '{print $1}'
}

psql_exec() {
  psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q -c "$1" >/dev/null
}

psql_query() {
  psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -t -A -c "$1"
}

setup_db() {
  local active_hash inactive_hash rate_hash
  active_hash="$(hash_key "$ACTIVE_KEY")"
  inactive_hash="$(hash_key "$INACTIVE_KEY")"
  rate_hash="$(hash_key "$RATE_KEY")"

  log "preparing postgres test data"
  psql_exec "CREATE EXTENSION IF NOT EXISTS pgcrypto;"
  psql_exec "CREATE TABLE IF NOT EXISTS api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key TEXT NOT NULL UNIQUE,
    name TEXT,
    rate_limit_per_minute INTEGER DEFAULT 60,
    is_active BOOLEAN DEFAULT true,
    created_at TIMESTAMP DEFAULT now()
  );"
  psql_exec "CREATE TABLE IF NOT EXISTS usage_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    api_key TEXT NOT NULL,
    model TEXT NOT NULL,
    prompt_tokens INT NOT NULL DEFAULT 0,
    completion_tokens INT NOT NULL DEFAULT 0,
    total_tokens INT NOT NULL DEFAULT 0,
    latency_ms INT NOT NULL DEFAULT 0,
    status_code INT NOT NULL DEFAULT 200,
    created_at TIMESTAMP NOT NULL DEFAULT now(),
    cost_usd NUMERIC(10, 8) NOT NULL DEFAULT 0
  );"
  psql_exec "CREATE INDEX IF NOT EXISTS idx_usage_api_key ON usage_logs(api_key);"
  psql_exec "CREATE INDEX IF NOT EXISTS idx_usage_created_at ON usage_logs(created_at);"
  psql_exec "INSERT INTO api_keys (key, is_active) VALUES ('$active_hash', true) ON CONFLICT (key) DO UPDATE SET is_active = EXCLUDED.is_active;"
  psql_exec "INSERT INTO api_keys (key, is_active) VALUES ('$inactive_hash', false) ON CONFLICT (key) DO UPDATE SET is_active = EXCLUDED.is_active;"
  psql_exec "INSERT INTO api_keys (key, is_active) VALUES ('$rate_hash', true) ON CONFLICT (key) DO UPDATE SET is_active = EXCLUDED.is_active;"
  psql_exec "DELETE FROM usage_logs WHERE api_key IN ('$active_hash', '$rate_hash');"
}

run_unit_tests() {
  if [[ "$RUN_UNIT_TESTS" -eq 1 ]]; then
    log "running go test ./..."
    (cd "$ROOT_DIR" && GOCACHE="$ROOT_DIR/.gocache" go test ./...)
  fi
}

start_server() {
  TMP_DIR="$(mktemp -d)"
  log "starting gateway on port $PORT"
  (
    cd "$ROOT_DIR"
    PORT="$PORT" RATE_LIMIT_PER_MINUTE="$RATE_LIMIT" OPENROUTER_API_KEY="$OPENROUTER_API_KEY" go run ./cmd/server
  ) >"$TMP_DIR/server.log" 2>&1 &
  SERVER_PID=$!

  for _ in $(seq 1 30); do
    if curl -sS -o /dev/null "http://127.0.0.1:$PORT/v1/chat/completions"; then
      return
    fi
    if ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
      cat "$TMP_DIR/server.log" >&2 || true
      fail "server exited during startup"
    fi
    sleep 1
  done

  cat "$TMP_DIR/server.log" >&2 || true
  fail "server did not become ready in time"
}

run_request() {
  local name="$1"
  local auth_header="$2"
  local body="$3"
  local body_file="$TMP_DIR/${name}.body"
  local header_file="$TMP_DIR/${name}.headers"
  local status_file="$TMP_DIR/${name}.status"

  if [[ -n "$auth_header" ]]; then
    curl -sS \
      -D "$header_file" \
      -o "$body_file" \
      -w '%{http_code}' \
      -X POST "http://127.0.0.1:$PORT/v1/chat/completions" \
      -H 'Content-Type: application/json' \
      -H "$auth_header" \
      --data "$body" >"$status_file"
  else
    curl -sS \
      -D "$header_file" \
      -o "$body_file" \
      -w '%{http_code}' \
      -X POST "http://127.0.0.1:$PORT/v1/chat/completions" \
      -H 'Content-Type: application/json' \
      --data "$body" >"$status_file"
  fi
}

status_of() {
  cat "$TMP_DIR/$1.status"
}

body_of() {
  cat "$TMP_DIR/$1.body"
}

headers_of() {
  cat "$TMP_DIR/$1.headers"
}

assert_status() {
  local name="$1"
  local expected="$2"
  local actual
  actual="$(status_of "$name")"
  [[ "$actual" == "$expected" ]] || fail "$name expected status $expected, got $actual"
}

assert_body_contains() {
  local name="$1"
  local needle="$2"
  grep -Fq "$needle" "$TMP_DIR/$name.body" || fail "$name response body missing: $needle"
}

assert_headers_contain() {
  local name="$1"
  local needle="$2"
  grep -Fiq "$needle" "$TMP_DIR/$name.headers" || fail "$name response headers missing: $needle"
}

test_auth_failures() {
  local payload
  payload='{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}'

  log "checking missing authorization header"
  run_request "missing_auth" "" "$payload"
  assert_status "missing_auth" "401"
  assert_body_contains "missing_auth" "missing authorization header"

  log "checking invalid authorization format"
  run_request "bad_scheme" "Authorization: Token nope" "$payload"
  assert_status "bad_scheme" "401"
  assert_body_contains "bad_scheme" "invalid authorization format"

  log "checking unknown api key"
  run_request "unknown_key" "Authorization: Bearer does-not-exist" "$payload"
  assert_status "unknown_key" "401"
  assert_body_contains "unknown_key" "invalid or inactive api key"

  log "checking inactive api key"
  run_request "inactive_key" "Authorization: Bearer $INACTIVE_KEY" "$payload"
  assert_status "inactive_key" "401"
  assert_body_contains "inactive_key" "invalid or inactive api key"
}

test_upstream_path() {
  local payload status
  payload='{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"reply with one word"}]}'

  log "checking authenticated request path through auth and proxy"
  run_request "upstream" "Authorization: Bearer $ACTIVE_KEY" "$payload"
  status="$(status_of "upstream")"

  case "$status" in
    200|400|401|402|403|404|429)
      ;;
    *)
      printf '[debug] upstream response body:\n%s\n' "$(body_of "upstream")" >&2
      fail "upstream check returned unexpected status $status"
      ;;
  esac

  assert_headers_contain "upstream" "X-RateLimit-Limit:"
  assert_headers_contain "upstream" "X-RateLimit-Remaining:"

  if [[ "$status" == "502" ]]; then
    fail "upstream request failed before reaching OpenRouter"
  fi
}

assert_usage_logged() {
  local raw_key="$1"
  local model="$2"
  local key_hash count
  key_hash="$(hash_key "$raw_key")"

  for _ in $(seq 1 20); do
    count="$(psql_query "SELECT COUNT(*) FROM usage_logs WHERE api_key = '$key_hash' AND model = '$model';" | tr -d '[:space:]')"
    if [[ -n "$count" ]] && [[ "$count" -ge 1 ]]; then
      return
    fi
    sleep 1
  done

  fail "expected usage_logs row for api key $key_hash and model $model"
}

test_rate_limit_precise() {
  local payload i status name
  payload='{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"ping"}]}'

  for i in $(seq 1 "$RATE_LIMIT"); do
    name="rate_ok_$i"
    run_request "$name" "Authorization: Bearer $RATE_KEY" "$payload"
    status="$(status_of "$name")"
    case "$status" in
      200|400|401|402|403|404|429)
        ;;
      *)
        fail "$name returned unexpected status $status"
        ;;
    esac
    assert_headers_contain "$name" "X-RateLimit-Limit:"
    assert_headers_contain "$name" "X-RateLimit-Remaining:"
  done

  run_request "rate_limited" "Authorization: Bearer $RATE_KEY" "$payload"
  assert_status "rate_limited" "429"
  assert_body_contains "rate_limited" "rate limit exceeded"
  assert_headers_contain "rate_limited" "Retry-After:"
}

main() {
  parse_args "$@"

  require_cmd go
  require_cmd curl
  require_cmd psql
  require_cmd sha256sum
  require_cmd redis-cli

  [[ "$RATE_LIMIT" =~ ^[1-9][0-9]*$ ]] || fail "--rate-limit must be a positive integer"
  [[ "$PORT" =~ ^[0-9]+$ ]] || fail "--port must be numeric"

  load_env

  log "checking redis connectivity"
  redis-cli -h 127.0.0.1 -p 6379 ping >/dev/null || fail "redis is not reachable on localhost:6379"

  run_unit_tests
  setup_db
  start_server
  test_auth_failures
  if [[ "$RUN_UPSTREAM_TEST" -eq 1 ]]; then
    test_upstream_path
    assert_usage_logged "$ACTIVE_KEY" "openai/gpt-4o-mini"
  fi
  test_rate_limit_precise

  log "all checks passed"
}

main "$@"
