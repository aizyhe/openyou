#!/usr/bin/env bash
set -euo pipefail
R='\033[31m'; G='\033[32m'; Y='\033[33m'; C='\033[36m'; N='\033[0m'
P=0; F=0; ok() { echo -e "${G}[PASS]${N} $1"; P=$((P+1)); }
fail() { echo -e "${R}[FAIL]${N} $1 — $2"; F=$((F+1)); }
info() { echo -e "${Y}[INFO]${N} $1"; }
step() { echo -e "\n${C}=== $1 ===${N}"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APK="$ROOT/android/app/build/outputs/apk/debug/app-debug.apk"
PKG="io.openyou.app"; PORT=18765

echo "=========================================="

step "1. Build & Install"
cd "$ROOT"
# "BUILD SUCCESSFUL" appears mid-output, not on the last line — old tail -1|grep failed every run.
BUILD_OUT=$(bash scripts/build.sh 2>&1) || { fail "build" "build.sh exited non-zero"; echo "$BUILD_OUT" | tail -20; exit 1; }
echo "$BUILD_OUT" | grep -q "BUILD SUCCESSFUL" && ok "APK build" || { fail "build" "no BUILD SUCCESSFUL in output"; echo "$BUILD_OUT" | tail -20; exit 1; }
adb install -r "$APK" 2>&1 | grep -q "Success" && ok "APK install" || fail "install" ""

# 2. Native shell
step "2. Native shell — startup / lifecycle / security"
adb shell am force-stop $PKG 2>/dev/null; sleep 1
adb shell am start -n $PKG/.MainActivity >/dev/null 2>&1; sleep 3
adb shell pidof $PKG >/dev/null 2>&1 && ok "process start" || fail "process" ""
adb shell dumpsys window 2>/dev/null | grep mCurrentFocus | grep -q "$PKG" && ok "foreground" || fail "foreground" ""

adb shell screencap -p /sdcard/_test.png 2>/dev/null
S=$(adb shell stat -c%s /sdcard/_test.png 2>/dev/null || echo "0")
[ "${S:-0}" -lt 10000 ] 2>/dev/null && ok "FLAG_SECURE" || fail "screenshot" "${S}bytes"

adb shell input keyevent KEYCODE_HOME 2>/dev/null; sleep 1
adb shell am start -n $PKG/.MainActivity >/dev/null 2>&1; sleep 2
adb shell pidof $PKG >/dev/null 2>&1 && ok "Home key resume" || fail "Home resume" ""

step "3. Daemon API — all endpoints"
adb forward tcp:$PORT tcp:8765 2>/dev/null || true; sleep 1

H=$(curl -s http://127.0.0.1:$PORT/api/health 2>/dev/null || "")
echo "$H"|grep -q '"status":"ok"' && ok "GET /api/health" || fail "health" "$H"

CHAT=$(curl -s -N -X POST http://127.0.0.1:$PORT/api/chat -H "Content-Type: application/json" -d '{"message":"hi","session_id":"t1"}' 2>/dev/null|head -3||echo "")
echo "$CHAT"|grep -q "data:" && ok "POST /api/chat SSE" || fail "chat SSE" "$CHAT"

curl -s http://127.0.0.1:$PORT/api/sessions 2>/dev/null|grep -q "t1" && ok "GET /api/sessions" || fail "sessions" ""
curl -s http://127.0.0.1:$PORT/api/sessions/t1 2>/dev/null|grep -q '"id"' && ok "GET /api/sessions/:id" || fail "detail" ""

# Docs moved under /api/docs/{path...} — the old /api/insides routes made these checks fail unconditionally.
curl -s -X PUT http://127.0.0.1:$PORT/api/docs/inside/t.md -H "Content-Type: application/json" -d '{"content":"x"}' >/dev/null 2>&1
curl -s http://127.0.0.1:$PORT/api/docs/inside/t.md 2>/dev/null|grep -q "x" && ok "inside doc CRUD" || fail "inside doc" ""
curl -s -X PUT http://127.0.0.1:$PORT/api/docs/rule/apk-test.md -H "Content-Type: application/json" -d '{"content":"r1"}' >/dev/null 2>&1
curl -s http://127.0.0.1:$PORT/api/docs/rule/apk-test.md 2>/dev/null|grep -q "r1" && ok "rule doc CRUD" || fail "rule doc" ""
curl -s http://127.0.0.1:$PORT/api/config 2>/dev/null|grep -q "api_key" && ok "GET /api/config" || fail "config" ""

step "4. WebView UI — DOM check"
LOGS=$(adb logcat -d -s chromium 2>/dev/null|grep -iE "openyou|console|loadUrl"|tail -5||echo "")
[ -n "$LOGS" ] && ok "WebView console" || info "WebView log empty (debug may be off)"

step "5. Robustness"
CRASHES=$(adb logcat -d 2>/dev/null|grep -iE "FATAL|$PKG.*panic|$PKG.*SIGSEGV"||echo "")
[ -z "$CRASHES" ] && ok "no crashes" || fail "crash" "$(echo "$CRASHES"|tail -3)"

M=$(adb shell dumpsys meminfo $PKG 2>/dev/null|grep "TOTAL PSS"|head -1|awk '{print $3}'||echo "0")
[ "${M:-0}" -lt 200000 ] 2>/dev/null && ok "memory PSS ${M}KB" || fail "memory" "${M}KB"

adb shell am force-stop $PKG 2>/dev/null; sleep 2
adb shell am start -n $PKG/.MainActivity >/dev/null 2>&1; sleep 4
REC=$(curl -s http://127.0.0.1:$PORT/api/health 2>/dev/null||echo "")
echo "$REC"|grep -q '"status":"ok"' && ok "kill+resume" || fail "resume" ""

# A failed `break` still left the loop exiting 0, so `done && ok` lied about failures — track it explicitly.
QUICK_OK=1
for i in 1 2 3; do
  adb shell am force-stop $PKG 2>/dev/null; sleep 1
  adb shell am start -n $PKG/.MainActivity >/dev/null 2>&1; sleep 2
  if ! adb shell pidof $PKG >/dev/null 2>&1; then fail "start#$i" ""; QUICK_OK=0; break; fi
done
[ "$QUICK_OK" = "1" ] && ok "3x quick start"

step "6. SPEC alignment"
check() { curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$PORT$1" 2>/dev/null||echo "000"; }
[ "$(check /api/health)" = "200" ] && ok "F-01 health" || fail "F-01" ""
[ "$(check /api/config)" = "200" ] && ok "F-02 config" || fail "F-02" ""
[ "$(check /api/sessions)" = "200" ] && ok "F-03 sessions" || fail "F-03" ""
# Routes aligned with the actual router (old /api/insides, /api/briefing are gone).
[ "$(check /api/docs)" = "200" ] && ok "F-04 context docs" || fail "F-04" ""
[ "$(check /api/home)" = "200" ] && ok "F-05 home briefing" || fail "F-05" ""

echo -e "\n${C}=========================================${N}"
printf "${G}Pass: %-3d${N}  ${R}Fail: %-3d${N}\n" $P $F
[ "$F" -eq 0 ] && echo -e "${G}ALL PASS${N}" && exit 0
echo -e "${R}FAILURES${N}" && exit 1
