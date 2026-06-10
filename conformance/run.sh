#!/usr/bin/env bash
# Cross-client conformance: build the daemon, start it, and run the same
# scenario (conformance/scenario.md) through every language client.
# A failure in any client is a parity break. Exit non-zero if any fail.
set -u
cd "$(dirname "$0")/.."

SOCKDIR=$(mktemp -d)
export PUPPTYEER_SOCK="$SOCKDIR/conf.sock"
trap 'rm -rf "$SOCKDIR"' EXIT

echo "building daemon…"
go build -o bin/pupptyeer ./cmd/pupptyeer || { echo "build failed"; exit 1; }

./bin/pupptyeer daemon >"$SOCKDIR/daemon.log" 2>&1 &
DPID=$!
trap 'kill $DPID 2>/dev/null; rm -rf "$SOCKDIR"' EXIT

# wait for the socket
for _ in $(seq 1 50); do [ -S "$PUPPTYEER_SOCK" ] && break; sleep 0.1; done

fail=0
run() {
  local name="$1"; shift
  if "$@"; then echo "  PASS $name"; else echo "  FAIL $name"; fail=1; fi
}

echo "running conformance against $PUPPTYEER_SOCK"
run go     go run ./conformance/go
run ts     node conformance/ts.mjs
run python python3 conformance/py.py

if [ "$fail" -eq 0 ]; then
  echo "conformance: ALL CLIENTS PASS"
else
  echo "conformance: PARITY BREAK - see failures above"
fi
exit $fail
