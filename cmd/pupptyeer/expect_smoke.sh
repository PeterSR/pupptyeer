#!/usr/bin/env bash
# Smoke test for `pupptyeer ctl expect`: proves all four triggers
# (regex, substr, idle, exit) plus the timeout exit code and --follow, driven
# against a real daemon over a temp socket. Self-contained: builds the binary,
# starts/stops its own daemon, honours $PUPPTYEER_SOCK like the other smokes.
#
# Usage: bash cmd/pupptyeer/expect_smoke.sh
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repo_root"

bin="$(mktemp -d)/pupptyeer"
go build -o "$bin" ./cmd/pupptyeer

work=$(mktemp -d)
export PUPPTYEER_SOCK="$work/daemon.sock"
trap 'kill "$daemon_pid" 2>/dev/null || true; rm -rf "$work" "$(dirname "$bin")"' EXIT

"$bin" daemon &
daemon_pid=$!

# Wait for the socket to appear.
for _ in $(seq 1 50); do
	[ -S "$PUPPTYEER_SOCK" ] && break
	sleep 0.1
done
[ -S "$PUPPTYEER_SOCK" ] || { echo "FAIL: daemon socket never appeared"; exit 1; }

pass=0 fail=0
# check <name> <expected-exit> <substring-in-stdout> -- <ctl expect args...>
# Spawns a fresh session (its command given via $cmd) and asserts the expect
# exit code and a stdout marker.
run_case() {
	local name=$1 want_code=$2 want_out=$3; shift 3
	[ "$1" = "--" ] && shift
	local id out code
	id=$("$bin" ctl new sh -c "$cmd")
	set +e
	out=$("$bin" ctl expect "$@" "$id"); code=$?
	set -e
	"$bin" ctl kill "$id" >/dev/null 2>&1 || true
	if [ "$code" = "$want_code" ] && [[ "$out" == *"$want_out"* ]]; then
		echo "ok   $name (exit=$code, out=${out//$'\n'/ })"
		pass=$((pass + 1))
	else
		echo "FAIL $name: want exit=$want_code out~='$want_out'; got exit=$code out='${out//$'\n'/ }'"
		fail=$((fail + 1))
	fi
}

# 1. regex matches a sentinel printed after a delay -> fired, exit 0.
cmd='sleep 1; printf "boot READY<<DONE>>\n"; sleep 30'
run_case "regex match" 0 "match regex=" -- --regex '<<DONE>>' --timeout 10s

# 2. literal substring -> fired, exit 0.
cmd='sleep 1; printf "result=hello-world\n"; sleep 30'
run_case "substr match" 0 "match substr=" -- --substr 'hello-world' --timeout 10s

# 3. output goes quiet -> idle fires, exit 0.
cmd='printf "warming up..."; sleep 30'
run_case "idle quiet" 0 "idle 1s" -- --idle 1s --timeout 10s

# 4. process exits -> exit trigger reports the code, exit 0.
cmd='sleep 1; exit 7'
run_case "process exit" 0 "exit code=7" -- --exit --timeout 10s

# 5. nothing matches within the deadline -> timeout, exit 2.
cmd='sleep 30'
run_case "timeout" 2 "timeout" -- --regex 'never-appears' --timeout 1s

# 6. --follow reports every hit, then ends at the deadline (exit 2). Two lines
#    printed with a gap; assert both matched before the timeout fired.
cmd='sleep 1; printf "LINE one\n"; sleep 1; printf "LINE two\n"; sleep 30'
id=$("$bin" ctl new sh -c "$cmd")
set +e
out=$("$bin" ctl expect --substr 'LINE' --follow --timeout 4s "$id"); code=$?
set -e
"$bin" ctl kill "$id" >/dev/null 2>&1 || true
hits=$(printf '%s\n' "$out" | grep -c 'match substr=' || true)
if [ "$code" = "2" ] && [ "$hits" = "2" ]; then
	echo "ok   follow (exit=$code, hits=$hits)"
	pass=$((pass + 1))
else
	echo "FAIL follow: want exit=2 hits=2; got exit=$code hits=$hits out='${out//$'\n'/ }'"
	fail=$((fail + 1))
fi

echo "---"
echo "passed=$pass failed=$fail"
[ "$fail" = "0" ]
