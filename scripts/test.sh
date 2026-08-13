#!/usr/bin/env bash
# No API key, no network, no emulator needed by default.
set -uo pipefail

G='\033[32m'; R='\033[31m'; Y='\033[33m'; N='\033[0m'
ok()   { echo -e "${G}[OK]${N} $1"; }
fail() { echo -e "${R}[FAIL]${N} $1"; }
info() { echo -e "${Y}==>${N} $1"; }

DAEMON="$(cd "$(dirname "$0")/../daemon" && pwd)"
cd "$DAEMON" || { fail "daemon dir not found"; exit 1; }

RACE=0; COVER=0; WANT_HTML=0; MIN=""
while [ $# -gt 0 ]; do
  case "$1" in
    --race) RACE=1 ;;
    --cover) COVER=1 ;;
    --html) WANT_HTML=1 ;;
    --min) shift; MIN="$1" ;;
  esac
  shift
done

rc=0

info "1/5 build"
if go build ./...; then ok "build"; else fail "build"; exit 1; fi

info "2/5 vet"
if go vet ./...; then ok "vet"; else fail "vet"; rc=1; fi

info "3/5 gofmt"
unformatted="$(gofmt -l . 2>/dev/null)"
if [ -z "$unformatted" ]; then ok "gofmt"; else fail "unformatted files:"; echo "$unformatted"; rc=1; fi

info "4/5 tests"
if [ "$COVER" = "1" ]; then
  COVER_OUT=/tmp/openyou-cover.out
  go test -coverpkg=./... -coverprofile="$COVER_OUT" ./... >/tmp/openyou-cover.log 2>&1
  trc=$?
  if [ $trc -ne 0 ]; then fail "tests"; tail -20 /tmp/openyou-cover.log; rc=1; else ok "tests"; fi
  echo
  echo "    per-package coverage:"
  grep -E 'coverage:' /tmp/openyou-cover.log | sed 's#openyou/daemon/##' | sed 's/^/      /'
  TOTAL=$(go tool cover -func="$COVER_OUT" | tail -1 | awk '{print $NF}')
  echo "    total: $TOTAL"
  echo "    lowest-covered functions:"
  go tool cover -func="$COVER_OUT" | grep -vE 'total:|100.0%' | sort -t$'\t' -k3 -n \
    | awk -F'\t' '{gsub(/ /,"",$3); if ($3+0 < 80) print "      "$3"\t"$1"\t"$2}' | head -15
  if [ "$WANT_HTML" = "1" ]; then
    go tool cover -html="$COVER_OUT" -o /tmp/openyou-coverage.html
    echo "    HTML report: /tmp/openyou-coverage.html"
  fi
  if [ -n "$MIN" ]; then
    num=$(echo "$TOTAL" | tr -d '%')
    if awk "BEGIN{exit !($num < $MIN)}"; then
      fail "coverage $TOTAL below required ${MIN}%"; rc=1
    else
      ok "coverage $TOTAL meets required ${MIN}%"
    fi
  fi
else
  if go test ./...; then ok "tests"; else fail "tests"; rc=1; fi
fi

if [ "$RACE" = "1" ]; then
  info "5/5 race detector"
  if go test -race ./...; then ok "race"; else fail "race"; rc=1; fi
else
  info "5/5 race detector — skipped (use --race)"
fi

echo
TESTS="$(grep -rh '^func Test' --include='*_test.go' . 2>/dev/null | wc -l | tr -d ' ')"
PKGS="$(go list ./... 2>/dev/null | wc -l | tr -d ' ')"
if [ "$rc" = "0" ]; then
  ok "ALL PASS — $PKGS packages, $TESTS test functions"
else
  fail "FAILURES above (exit $rc)"
fi
exit $rc
