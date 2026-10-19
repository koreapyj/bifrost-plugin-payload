#!/usr/bin/env bash
# End-to-end test for the payload plugin.
#
# Starts the mock upstream (e2e/mock-upstream) and a dynamically linked
# bifrost-http that loads payload.so, sends requests through Bifrost and checks
# the bodies the mock received. Prints PASS/FAIL per check and exits non-zero
# when any check fails.
#
# Required environment:
#   BIFROST_HTTP   path to a dynamically linked bifrost-http binary
#   PAYLOAD_SO     path to payload.so built against the same Bifrost tree
#
# Optional environment:
#   E2E_WORKDIR          scratch directory (default: $RUNNER_TEMP/payload-e2e or
#                        <repo>/.tmp/e2e); recreated on every run
#   MOCK_UPSTREAM        prebuilt mock-upstream binary (default: built with go)
#   BIFROST_PORT         port for bifrost-http (default: a free random port)
#   E2E_STARTUP_TIMEOUT  seconds to wait for bifrost-http (default: 180)
#   JQ                   jq binary (default: jq)
#   E2E_VERBOSE=1        print response bodies of failing checks in full
# jq programs are single-quoted on purpose.
# shellcheck disable=SC2016
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
JQ=${JQ:-jq}
STARTUP_TIMEOUT=${E2E_STARTUP_TIMEOUT:-180}
ADMIN_USER="admin"
ADMIN_PASSWORD=e2e-admin-password
VK_MAIN=sk-bf-e2e-main
VK_OTHER=sk-bf-e2e-other
UI_PATH=/v1/models/_payload/ui

die() {
	echo "e2e: $*" >&2
	exit 2
}

abspath() {
	local p=$1
	[[ -e $p ]] || die "not found: $p"
	echo "$(cd "$(dirname "$p")" && pwd)/$(basename "$p")"
}

command -v curl >/dev/null || die "curl is required"
command -v "$JQ" >/dev/null || die "jq is required (set JQ=/path/to/jq)"
[[ -n ${BIFROST_HTTP:-} ]] || die "BIFROST_HTTP must point to a dynamically linked bifrost-http binary"
[[ -n ${PAYLOAD_SO:-} ]] || die "PAYLOAD_SO must point to payload.so"
BIFROST_HTTP=$(abspath "$BIFROST_HTTP")
PAYLOAD_SO=$(abspath "$PAYLOAD_SO")

WORK=${E2E_WORKDIR:-}
if [[ -z $WORK ]]; then
	if [[ -n ${RUNNER_TEMP:-} ]]; then
		WORK=$RUNNER_TEMP/payload-e2e
	else
		WORK=$ROOT/.tmp/e2e
	fi
fi
rm -rf "$WORK"
mkdir -p "$WORK/app" "$WORK/out"
WORK=$(cd "$WORK" && pwd)
OUT=$WORK/out
COOKIES=$WORK/cookies.txt

MOCK_PID=""
BIFROST_PID=""
cleanup() {
	local code=$?
	local pid
	for pid in $BIFROST_PID $MOCK_PID; do
		kill "$pid" 2>/dev/null || true
	done
	for pid in $BIFROST_PID $MOCK_PID; do
		wait "$pid" 2>/dev/null || true
	done
	if [[ $code -ne 0 ]]; then
		echo "--- last 60 lines of bifrost.log ($WORK/bifrost.log) ---" >&2
		tail -n 60 "$WORK/bifrost.log" 2>/dev/null >&2 || true
	fi
	exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# ---------------------------------------------------------------- mock upstream
if [[ -n ${MOCK_UPSTREAM:-} ]]; then
	MOCK_BIN=$(abspath "$MOCK_UPSTREAM")
else
	command -v go >/dev/null || die "go is required to build the mock upstream (or set MOCK_UPSTREAM)"
	MOCK_BIN=$WORK/mock-upstream
	(cd "$ROOT" && GOWORK=off CGO_ENABLED=0 go build -trimpath -o "$MOCK_BIN" ./e2e/mock-upstream)
fi
"$MOCK_BIN" -addr 127.0.0.1:0 -port-file "$WORK/mock.port" -record "$WORK/mock.jsonl" >"$WORK/mock.log" 2>&1 &
MOCK_PID=$!
for _ in $(seq 1 100); do
	[[ -s $WORK/mock.port ]] && break
	sleep 0.1
done
[[ -s $WORK/mock.port ]] || die "mock upstream did not start (see $WORK/mock.log)"
MOCK=http://127.0.0.1:$(tr -d '[:space:]' <"$WORK/mock.port")

# ---------------------------------------------------------------- bifrost-http
port_in_use() { (echo >"/dev/tcp/127.0.0.1/$1") >/dev/null 2>&1; }
if [[ -z ${BIFROST_PORT:-} ]]; then
	for _ in $(seq 1 50); do
		BIFROST_PORT=$((20000 + RANDOM % 30000))
		port_in_use "$BIFROST_PORT" || break
	done
fi
BIFROST=http://127.0.0.1:$BIFROST_PORT

sed -e "s|@MOCK_URL@|$MOCK|g" \
	-e "s|@PAYLOAD_SO@|$PAYLOAD_SO|g" \
	-e "s|@BIFROST_URL@|$BIFROST|g" \
	-e "s|@ADMIN_USER@|$ADMIN_USER|g" \
	-e "s|@ADMIN_PASSWORD@|$ADMIN_PASSWORD|g" \
	"$ROOT/e2e/config.json" >"$WORK/app/config.json"
"$JQ" -e . "$WORK/app/config.json" >/dev/null || die "rendered config.json is not valid JSON"

"$BIFROST_HTTP" -app-dir "$WORK/app" -host 127.0.0.1 -port "$BIFROST_PORT" -log-style pretty -log-level info \
	>"$WORK/bifrost.log" 2>&1 &
BIFROST_PID=$!
ready=0
for _ in $(seq 1 "$STARTUP_TIMEOUT"); do
	if ! kill -0 "$BIFROST_PID" 2>/dev/null; then
		die "bifrost-http exited during startup (see $WORK/bifrost.log)"
	fi
	if curl -fsS -o /dev/null "$BIFROST/health" 2>/dev/null; then
		ready=1
		break
	fi
	sleep 1
done
[[ $ready -eq 1 ]] || die "bifrost-http not ready after ${STARTUP_TIMEOUT}s"
echo "e2e: mock=$MOCK bifrost=$BIFROST workdir=$WORK"

# ---------------------------------------------------------------- helpers
PASSED=0
FAILED=0
FAILED_NAMES=()
pass() {
	PASSED=$((PASSED + 1))
	echo "PASS  $1"
}
fail() {
	FAILED=$((FAILED + 1))
	FAILED_NAMES+=("$1")
	echo "FAIL  $1${2:+ -- $2}"
}

# show FILE: print (a prefix of) a file for diagnostics.
show() {
	if [[ ${E2E_VERBOSE:-0} == 1 ]]; then
		sed 's/^/      | /' "$1"
	else
		head -c 1500 "$1" | sed 's/^/      | /'
		echo
	fi
}

# check_jq NAME FILE FILTER [jq args...]: pass when FILTER yields true.
check_jq() {
	local name=$1 file=$2 filter=$3
	shift 3
	if "$JQ" -e "$@" "$filter" "$file" >/dev/null 2>&1; then
		pass "$name"
	else
		fail "$name" "jq: $filter"
		show "$file"
	fi
}

# check_eq NAME ACTUAL EXPECTED
check_eq() {
	if [[ $2 == "$3" ]]; then
		pass "$1"
	else
		fail "$1" "got '$2', want '$3'"
	fi
}

clear_records() { curl -fsS -X DELETE "$MOCK/__records" >/dev/null; }

# posts OUTFILE: mock records of POST requests (JSON array) since the last clear.
posts() { curl -fsS "$MOCK/__records" | "$JQ" '[.[] | select(.method == "POST")]' >"$1"; }

# last_body OUTFILE [PATH]: body of the last recorded POST (optionally to PATH).
last_body() {
	local path=${2:-}
	curl -fsS "$MOCK/__records" |
		"$JQ" --arg p "$path" '[.[] | select(.method == "POST" and ($p == "" or .path == $p))] | last | .body // null' >"$1"
}

# infer NAME CASE VK URLPATH BODY [curl args...]: POST an inference request with
# the x-e2e case header; response body in $OUT/NAME.resp, status in $OUT/NAME.code.
infer() {
	local name=$1 tcase=$2 vk=$3 path=$4 body=$5
	shift 5
	local args=(-sS -o "$OUT/$name.resp" -w '%{http_code}' -X POST "$BIFROST$path"
		-H 'Content-Type: application/json' -H "x-e2e: $tcase" --data-binary "$body")
	[[ -n $vk ]] && args+=(-H "x-bf-vk: $vk")
	curl "${args[@]}" "$@" >"$OUT/$name.code" || echo 000 >"$OUT/$name.code"
}

# chat NAME CASE VK BODY: send a request and capture the upstream body in $OUT/NAME.body.
chat() {
	local name=$1 tcase=$2 vk=$3 body=$4
	clear_records
	infer "$name" "$tcase" "$vk" /v1/chat/completions "$body"
	last_body "$OUT/$name.body"
	local code
	code=$(cat "$OUT/$name.code")
	if [[ $code != 200 ]]; then
		fail "$name: HTTP status" "got $code"
		show "$OUT/$name.resp"
	fi
}

# capture FILE URL [curl args...]: GET URL and write a normalized dump of the
# status line, headers and body. Values of headers that Bifrost mints per
# request (Date, request/trace ids) are replaced by a fixed token; the header
# names and their order are kept so a missing or extra header is still a diff.
capture() {
	local file=$1 url=$2
	shift 2
	local raw=$file.raw
	curl -sS -D "$raw.h" -o "$raw.b" "$@" "$url" || true
	{
		tr -d '\r' <"$raw.h" | sed -E 's/^(Date|X-Bifrost-Request-Id|X-Request-Id|X-Bifrost-Trace-Id):.*/\1: <per-request>/I'
		echo "--- body ---"
		cat "$raw.b"
	} >"$file"
}

# check_same NAME FILE_A FILE_B
check_same() {
	if cmp -s "$2" "$3"; then
		pass "$1"
	else
		fail "$1" "responses differ"
		diff -u "$2" "$3" | head -n 40 | sed 's/^/      | /' || true
	fi
}

login() {
	local code
	code=$(curl -sS -c "$COOKIES" -o "$OUT/login.resp" -w '%{http_code}' -X POST "$BIFROST/api/session/login" \
		-H 'Content-Type: application/json' \
		--data-binary "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASSWORD\"}")
	[[ $code == 200 ]] || {
		show "$OUT/login.resp"
		die "dashboard login failed with HTTP $code"
	}
	TOKEN=$(awk '$6 == "token" { print $7 }' "$COOKIES" | tail -n 1)
	[[ -n $TOKEN ]] || die "login did not set the token cookie"
}

# api METHOD PATH OUTFILE [curl args...]: authenticated management API call; prints status.
api() {
	local method=$1 path=$2 out=$3
	shift 3
	curl -sS -b "$COOKIES" -o "$out" -w '%{http_code}' -X "$method" "$BIFROST$path" "$@" || echo 000
}

# put_config NAME CONFIG_FILE: PUT /api/plugins/payload keeping enabled/path/placement/order.
put_config() {
	local name=$1 cfg=$2
	"$JQ" --slurpfile cfg "$cfg" '{enabled, path} + (if .placement then {placement} else {} end)
		+ (if .order != null then {order} else {} end) + {config: $cfg[0]}' "$OUT/plugin.orig.json" >"$OUT/$name.put.json"
	api PUT /api/plugins/payload "$OUT/$name.put.resp" -H 'Content-Type: application/json' \
		--data-binary "@$OUT/$name.put.json"
}

# ---------------------------------------------------------------- setup
TOKEN=""
login
code=$(api GET /api/plugins/payload "$OUT/plugin.orig.json")
[[ $code == 200 ]] || die "GET /api/plugins/payload returned $code"
status=$("$JQ" -r '.status.status // empty' "$OUT/plugin.orig.json")
if [[ $status != active ]]; then
	show "$OUT/plugin.orig.json"
	die "payload plugin is not active (status: ${status:-unknown})"
fi
"$JQ" '.config' "$OUT/plugin.orig.json" >"$OUT/config.orig.json"
echo "e2e: payload plugin active"

# ---------------------------------------------------------------- (a) http stage on /v1/chat/completions
chat basic basic "$VK_MAIN" '{"model":"openai/gpt-mock","messages":[{"role":"user","content":"hi"}],"metadata":{"client":"e2e"},"temperature":0.5,"user":"drop-me"}'
check_jq "http: default writes a missing value" "$OUT/basic.body" '.metadata.http_default == "d"'
check_jq "http: default keeps an existing value" "$OUT/basic.body" '.temperature == 0.5'
check_jq "http: override sets a value" "$OUT/basic.body" '.top_p == 0.25'
check_jq "http: override replaces a nested value" "$OUT/basic.body" '.metadata.client == "overridden"'
check_jq "http: filter removes a value" "$OUT/basic.body" 'has("user") | not'
check_jq "http: disabled rule is ignored" "$OUT/basic.body" '.metadata | has("disabled_rule") | not'

chat prio prio "$VK_MAIN" '{"model":"openai/gpt-mock","messages":[{"role":"user","content":"hi"}]}'
check_jq "priority: lowest priority wins" "$OUT/prio.body" '.metadata.winner == "first"'
check_jq "first-match: second matching rule not applied" "$OUT/prio.body" '.metadata | has("second_applied") | not'

chat chain chain "$VK_MAIN" '{"model":"openai/gpt-mock","messages":[{"role":"user","content":"hi"}]}'
check_jq "chain_rule: both chained rules applied" "$OUT/chain.body" '.metadata.chain_a == "1" and .metadata.chain_b == "1"'

chat scope_vk scope "$VK_MAIN" '{"model":"openai/gpt-mock","messages":[{"role":"user","content":"hi"}]}'
check_jq "scope: virtual_key rule wins for its key" "$OUT/scope_vk.body" '.metadata.scope == "vk"'
chat scope_other scope "$VK_OTHER" '{"model":"openai/gpt-mock","messages":[{"role":"user","content":"hi"}]}'
check_jq "scope: other key falls through to global" "$OUT/scope_other.body" '.metadata.scope == "global"'

# ---------------------------------------------------------------- (e) every hook ran
chat hooks hooks "$VK_MAIN" '{"model":"openai/gpt-mock","messages":[{"role":"user","content":"hi"}],"metadata":{"client":"e2e"}}'
check_jq "hooks: http stage ran" "$OUT/hooks.body" '.metadata.h_http == "1"'
check_jq "hooks: request stage ran" "$OUT/hooks.body" '.metadata.h_request == "1"'
check_jq "hooks: llm stage ran" "$OUT/hooks.body" '.metadata.h_llm == "1"'
check_jq "hooks: llm extra param passed through" "$OUT/hooks.body" '.x_llm_extra == "1"'

# ---------------------------------------------------------------- (c) provider-specific llm rules
chat prov_openai provider "$VK_MAIN" '{"model":"openai/gpt-mock","messages":[{"role":"user","content":"hi"}],"temperature":0.7}'
check_jq "llm: openai rule applied to openai" "$OUT/prov_openai.body" '.top_p == 0.321'
check_jq "llm: anthropic rule not applied to openai" "$OUT/prov_openai.body" '.temperature == 0.7'
clear_records
infer prov_anthropic provider "$VK_MAIN" /v1/chat/completions \
	'{"model":"anthropic/claude-mock","max_completion_tokens":16,"messages":[{"role":"user","content":"hi"}],"temperature":0.7}'
last_body "$OUT/prov_anthropic.body" /v1/messages
check_eq "llm: anthropic request status" "$(cat "$OUT/prov_anthropic.code")" 200
check_jq "llm: anthropic rule applied to anthropic" "$OUT/prov_anthropic.body" '.temperature == 0.123'
check_jq "llm: openai rule not applied to anthropic" "$OUT/prov_anthropic.body" '. != null and (has("top_p") | not)'

# ---------------------------------------------------------------- (d) fallback isolation
clear_records
infer fallback fallback "$VK_MAIN" /v1/chat/completions \
	'{"model":"failprov/gpt-mock","fallbacks":["openai/gpt-mock"],"messages":[{"role":"user","content":"hi"}]}'
posts "$OUT/fallback.records"
check_eq "fallback: request succeeds via fallback" "$(cat "$OUT/fallback.code")" 200
check_jq "fallback: primary attempt reached the failing mock" "$OUT/fallback.records" \
	'[.[] | select(.failed)] | length == 1'
check_jq "fallback: llm rule applied to primary attempt" "$OUT/fallback.records" \
	'[.[] | select(.failed)][0].body | .metadata.primary_only == "1" and .x_primary_extra == "1"'
check_jq "fallback: secondary attempt reached openai" "$OUT/fallback.records" \
	'[.[] | select((.failed | not) and .path == "/v1/chat/completions")] | length == 1'
check_jq "fallback: secondary body has no primary-only modification" "$OUT/fallback.records" \
	'[.[] | select((.failed | not) and .path == "/v1/chat/completions")][0].body_raw
	 | (contains("primary_only") or contains("x_primary_extra")) | not'

# ---------------------------------------------------------------- (b) raw body on /anthropic/v1/messages
# Bifrost only forwards the client body verbatim (UseRawRequestBody) for Claude
# Code clients, hence the user agent.
RAW_REQ='{"model":"claude-mock","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"metadata":{"user_id":"orig"},"x_client_field":{"keep":true}}'
clear_records
infer raw raw "$VK_MAIN" /anthropic/v1/messages "$RAW_REQ" \
	-H 'anthropic-version: 2023-06-01' -H 'User-Agent: claude-cli/2.1.0 (external, cli)'
last_body "$OUT/raw.body" /v1/messages
check_eq "raw: request status" "$(cat "$OUT/raw.code")" 200
echo "$RAW_REQ" | "$JQ" '. + {stop_sequences: ["END"], x_raw_marker: {a: [1, 2]}} | .metadata.user_id = "raw-user"' >"$OUT/raw.expected"
check_jq "raw: upstream body equals client body plus http+llm edits" "$OUT/raw.body" \
	'(. | del(.model)) == ($want[0] | del(.model))' --slurpfile want "$OUT/raw.expected"
check_jq "raw: model reaches upstream" "$OUT/raw.body" '.model == "claude-mock"'

# ---------------------------------------------------------------- (f) UI smoke
capture "$OUT/ui.page" "$BIFROST$UI_PATH" -b "token=$TOKEN"
if head -n 1 "$OUT/ui.page" | grep -q ' 200'; then pass "ui: page served with a valid session"; else fail "ui: page served with a valid session" "$(head -n 1 "$OUT/ui.page")"; fi
if grep -qi '^content-type: text/html' "$OUT/ui.page"; then pass "ui: page is text/html"; else fail "ui: page is text/html"; fi
if grep -qi '^cache-control:.*no-store' "$OUT/ui.page"; then pass "ui: page has Cache-Control: no-store"; else fail "ui: page has Cache-Control: no-store"; fi
capture "$OUT/ui.wasm" "$BIFROST$UI_PATH/engine.wasm" -b "token=$TOKEN" -H 'Accept-Encoding: gzip'
if head -n 1 "$OUT/ui.wasm" | grep -q ' 200' && grep -qi '^content-type: application/wasm' "$OUT/ui.wasm"; then
	pass "ui: engine.wasm is application/wasm"
else
	fail "ui: engine.wasm is application/wasm"
	sed -n '1,/^--- body ---$/p' "$OUT/ui.wasm" | sed 's/^/      | /'
fi
for asset in app.js wasm_exec.js; do
	capture "$OUT/ui.$asset" "$BIFROST$UI_PATH/$asset" -b "token=$TOKEN"
	if head -n 1 "$OUT/ui.$asset" | grep -q ' 200' && grep -qi '^content-type:.*javascript' "$OUT/ui.$asset"; then
		pass "ui: $asset served"
	else
		fail "ui: $asset served"
	fi
done

# ---------------------------------------------------------------- (g) stealth: no token / invalid token
REF=$BIFROST/v1/models/nonexistent-xyz
BAD_TOKEN=00000000-0000-4000-8000-000000000000
capture "$OUT/ctl.a" "$REF"
capture "$OUT/ctl.b" "$REF"
check_same "stealth control: reference response is stable after normalization" "$OUT/ctl.a" "$OUT/ctl.b"

stealth_case() {
	local label=$1
	shift
	capture "$OUT/st.ui" "$BIFROST$UI_PATH" "$@"
	capture "$OUT/st.ref" "$REF" "$@"
	check_same "stealth ($label): UI page identical to unknown model" "$OUT/st.ref" "$OUT/st.ui"
	capture "$OUT/st.ui" "$BIFROST$UI_PATH/engine.wasm" "$@"
	capture "$OUT/st.ref" "$REF/engine.wasm" "$@"
	check_same "stealth ($label): UI asset identical to unknown model" "$OUT/st.ref" "$OUT/st.ui"
}
stealth_case "no token"
stealth_case "no token, virtual key" -H "x-bf-vk: $VK_MAIN"
stealth_case "invalid cookie" -b "token=$BAD_TOKEN"
stealth_case "invalid bearer" -H "Authorization: Bearer $BAD_TOKEN"

# ---------------------------------------------------------------- (h) hot reload
chat reload_before reload "$VK_MAIN" '{"model":"openai/gpt-mock","messages":[{"role":"user","content":"hi"}]}'
check_jq "reload: new rule absent before PUT" "$OUT/reload_before.body" '(.metadata.reloaded // null) == null'
"$JQ" '.rules += [{name: "reload-http", enabled: true, stage: "http", scope: "global", scope_id: null,
	priority: 50, chain_rule: false, cel_expression: "headers[\"x-e2e\"] == \"reload\"", query: null,
	actions: {override: {"metadata.reloaded": "\"yes\""}}}]' "$OUT/config.orig.json" >"$OUT/config.reload.json"
code=$(put_config reload "$OUT/config.reload.json")
check_eq "reload: PUT /api/plugins/payload" "$code" 200
chat reload_after reload "$VK_MAIN" '{"model":"openai/gpt-mock","messages":[{"role":"user","content":"hi"}]}'
check_jq "reload: new rule applied after PUT" "$OUT/reload_after.body" '.metadata.reloaded == "yes"'

"$JQ" '.rules += [{name: "broken", enabled: true, stage: "http", scope: "global", scope_id: null,
	priority: 51, chain_rule: false, cel_expression: "headers[", query: null, actions: {}}]' \
	"$OUT/config.reload.json" >"$OUT/config.broken.json"
code=$(put_config broken "$OUT/config.broken.json")
if [[ $code != 200 ]]; then pass "reload: invalid config rejected (HTTP $code)"; else fail "reload: invalid config rejected"; fi
chat reload_kept reload "$VK_MAIN" '{"model":"openai/gpt-mock","messages":[{"role":"user","content":"hi"}]}'
check_jq "reload: previous rules kept after a rejected config" "$OUT/reload_kept.body" '.metadata.reloaded == "yes"'

# ---------------------------------------------------------------- (g) stealth: api_base unset
# Uses the VALID session cookie: the UI must stay closed without api_base even
# though the same token opened it before (no gate decision survives re-Init).
"$JQ" '.ui |= del(.api_base)' "$OUT/config.orig.json" >"$OUT/config.nobase.json"
code=$(put_config nobase "$OUT/config.nobase.json")
check_eq "stealth (api_base unset): PUT config without api_base" "$code" 200
capture "$OUT/nb.ui" "$BIFROST$UI_PATH" -b "token=$TOKEN"
capture "$OUT/nb.ref" "$REF" -b "token=$TOKEN"
check_same "stealth (api_base unset, valid token): UI page identical to unknown model" "$OUT/nb.ref" "$OUT/nb.ui"

code=$(put_config restore "$OUT/config.orig.json")
check_eq "restore: PUT original config" "$code" 200
capture "$OUT/ui.again" "$BIFROST$UI_PATH" -b "token=$TOKEN"
if head -n 1 "$OUT/ui.again" | grep -q ' 200'; then pass "restore: UI reachable again"; else fail "restore: UI reachable again"; fi

# ---------------------------------------------------------------- summary
echo
echo "e2e: $PASSED passed, $FAILED failed"
if [[ $FAILED -gt 0 ]]; then
	printf '  failed: %s\n' "${FAILED_NAMES[@]}"
	exit 1
fi
