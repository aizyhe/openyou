#!/usr/bin/env bash
# Default: fake OpenAI-compatible LLM server, so the e2e runs with no API key; --live uses real DeepSeek.
set -uo pipefail
G='\033[32m'; R='\033[31m'; Y='\033[33m'; N='\033[0m'
ok()   { echo -e "${G}[OK]${N} $1"; }
fail() { echo -e "${R}[FAIL]${N} $1"; FAILED=1; }
info() { echo -e "${Y}==>${N} $1"; }

LIVE=0
for a in "$@"; do case "$a" in --live) LIVE=1 ;; esac; done

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DAEMON="$ROOT/daemon"
PORT=8765
LLM_PORT=9917
DATA="$(mktemp -d)"
FAILED=0
DAEMON_PID=""; LLM_PID=""

cleanup() {
  [ -n "$DAEMON_PID" ] && kill "$DAEMON_PID" 2>/dev/null
  [ -n "$LLM_PID" ] && kill "$LLM_PID" 2>/dev/null
  rm -rf "$DATA" /tmp/fakellm_e2e.go 2>/dev/null
  wait 2>/dev/null
}
trap cleanup EXIT

if [ "$LIVE" = "1" ]; then
  API_KEY="${DEEPSEEK_API_KEY:-}"
  if [ -z "$API_KEY" ] && [ -f "$HOME/.opencode/opencode.json" ]; then
    API_KEY=$(grep -oP '"apiKey"\s*:\s*"\K[^"]+' "$HOME/.opencode/opencode.json" 2>/dev/null || echo "")
  fi
  if [ -z "$API_KEY" ]; then
    fail "DEEPSEEK_API_KEY not set (set env var or ensure ~/.opencode/opencode.json exists)"; exit 1
  fi
  BASE_URL="https://api.deepseek.com/v1"
  MODEL="deepseek-v4-pro"
  info "live mode — DeepSeek ($MODEL)"
else
  info "fake mode — starting local fake LLM server"
  cat > /tmp/fakellm_e2e.go <<'GOEOF'
package main
import("encoding/json";"fmt";"io";"net/http";"strings")
func main(){
 http.HandleFunc("/v1/chat/completions",func(w http.ResponseWriter,r *http.Request){
  b,_:=io.ReadAll(r.Body)
  var req struct{Messages []struct{Role,Content string}`json:"messages"`;Stream bool`json:"stream"`}
  json.Unmarshal(b,&req)
  joined:="";for _,m:=range req.Messages{joined+=m.Content+"\n"}
  reply:="OK, noted."
  if strings.Contains(joined,"Li Lei"){reply="Your name is Li Lei."}
  if req.Stream{
   w.Header().Set("Content-Type","text/event-stream")
   for _,r:=range reply{
    fmt.Fprintf(w,"data: %s\n\n",j(map[string]any{"choices":[]any{map[string]any{"delta":map[string]string{"content":string(r)}}}}))
   }
   fmt.Fprint(w,"data: [DONE]\n\n");return
  }
  fmt.Fprint(w,j(map[string]any{"choices":[]any{map[string]any{"message":map[string]string{"role":"assistant","content":reply}}}}))
 })
 http.ListenAndServe("127.0.0.1:9917",nil)
}
func j(v any)string{b,_:=json.Marshal(v);return string(b)}
GOEOF
  ( cd /tmp && go run fakellm_e2e.go ) & LLM_PID=$!
  for i in $(seq 1 20); do curl -s "http://127.0.0.1:$LLM_PORT/v1/chat/completions" -d '{}' >/dev/null 2>&1 && break; sleep 0.3; done
  API_KEY="test"
  BASE_URL="http://127.0.0.1:$LLM_PORT/v1"
  MODEL="fake"
fi

# Guard: a dev daemon already on 8765 would silently answer every check below.
if curl -s "http://127.0.0.1:$PORT/api/health" 2>/dev/null | grep -q '"status":"ok"'; then
  fail "port $PORT already serves a daemon — stop it before running e2e"
  exit 1
fi

info "building daemon"
( cd "$DAEMON" && go build -o "$DATA/daemon" ./cmd/daemon/ ) || { fail "build"; exit 1; }
ok "build"

info "starting daemon (data=$DATA/openyou)"
OPENYOU_DATA_DIR="$DATA/openyou" "$DATA/daemon" >"$DATA/daemon.log" 2>&1 & DAEMON_PID=$!
for i in $(seq 1 30); do
  curl -s "http://127.0.0.1:$PORT/api/health" 2>/dev/null | grep -q '"status":"ok"' && break; sleep 0.3
done
curl -s "http://127.0.0.1:$PORT/api/health" | grep -q '"status":"ok"' && ok "daemon up" || { fail "daemon did not start"; cat "$DATA/daemon.log"; exit 1; }

# Mutating endpoints require Content-Type: application/json (CSRF guard).
curl -s -X PUT "http://127.0.0.1:$PORT/api/config" \
  -H "Content-Type: application/json" \
  -d "{\"api_key\":\"$API_KEY\",\"base_url\":\"$BASE_URL\",\"model\":\"$MODEL\"}" >/dev/null
ok "configured"

SID="e2e_fullproc"
info "turn 1: tell name"
curl -s -N -X POST "http://127.0.0.1:$PORT/api/chat" \
  -H "Content-Type: application/json" \
  -d "{\"message\":\"My name is Li Lei\",\"session_id\":\"$SID\"}" >/dev/null
info "turn 2: ask name (no name in this message — only history has it)"
R2=$(curl -s -N -X POST "http://127.0.0.1:$PORT/api/chat" \
  -H "Content-Type: application/json" \
  -d "{\"message\":\"What is my name\",\"session_id\":\"$SID\"}")
echo "$R2" | grep -qi "li lei\|Li Lei" && ok "continuous memory" || { fail "did not recall name across turns"; echo "  got: $(echo "$R2"|tr -d '\n'|head -c200)"; }

curl -s "http://127.0.0.1:$PORT/api/sessions" | grep -q "$SID" && ok "session persisted" || fail "session not persisted"
R3=$(curl -s -N -X POST "http://127.0.0.1:$PORT/api/chat" -H "Content-Type: application/json" -d '{"message":"What is my name","session_id":"other"}')
echo "$R3" | grep -qi "li lei" && fail "session isolation broken" || ok "session isolation"

# Old /api/insides & /api/briefing routes are gone — these checks used to fail unconditionally.
curl -s -X PUT "http://127.0.0.1:$PORT/api/docs/rule/e2e-test.md" -H "Content-Type: application/json" -d '{"content":"#1 Keep it brief"}' >/dev/null
curl -s "http://127.0.0.1:$PORT/api/docs/rule/e2e-test.md" | grep -q 'Keep it brief' && ok "docs CRUD" || fail "docs CRUD"
curl -s "http://127.0.0.1:$PORT/api/home" | grep -q 'people_count\|rules_count' && ok "home briefing" || fail "home briefing"
curl -s "http://127.0.0.1:$PORT/api/skills" | grep -q 'skills' && ok "skills list" || fail "skills list"

# Export is a zip (userdata dir verbatim + manifest.json), not a single JSON anymore.
curl -s -X GET "http://127.0.0.1:$PORT/api/backup/export" -o "$DATA/openyou-e2e-backup.zip"
unzip -l "$DATA/openyou-e2e-backup.zip" | grep -q 'userdata/manifest.json' && ok "backup export (zip)" || fail "backup export"
# Import back into the same daemon — must be idempotent.
curl -s -X POST "http://127.0.0.1:$PORT/api/backup/import" -H "Content-Type: application/json" \
  --data-binary @"$DATA/openyou-e2e-backup.zip" | grep -q '"ok":"true"' && ok "backup import (zip)" || fail "backup import"

echo
if [ "$FAILED" = "0" ]; then ok "FULL-PROCESS E2E: ALL PASS"; else fail "FAILURES above"; fi
exit $FAILED
