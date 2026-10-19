#!/bin/sh
# Start the pupptyeer daemon in the background, wait for its socket, then exec
# the command. Run under an init (tini -g, or docker --init) so the daemon and
# its pty children die with the container. Self-contained: finds its sibling
# binaries by its own location, so it works from any directory it is copied to.
#
# Usage: pupptyeer-entrypoint COMMAND [ARGS...]   (no command: an interactive sh)
set -eu

BIN_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
# Sessions started through the daemon resolve bare names through PATH, so the
# binaries must be on it here and in the daemon, also on a base that did not
# set PATH.
PATH="$BIN_DIR:$PATH"
export PATH

SOCK="${PUPPTYEER_SOCK:-/tmp/pupptyeer/daemon.sock}"
export PUPPTYEER_SOCK="$SOCK"
SOCK_DIR="$(dirname "$SOCK")"
# Only a directory made here is made private: chmod on an existing one (/tmp,
# say) would be wrong or fail.
if [ ! -d "$SOCK_DIR" ]; then
  mkdir -p -m 0700 "$SOCK_DIR"
fi

if [ "$#" -eq 0 ]; then
  set -- sh
fi

# A daemon already answering on the socket (the helper run again through
# `docker exec`, say) is reused. Removing its socket would orphan it and every
# session it holds, which the daemon itself refuses to do.
if "$BIN_DIR/pupptyeer" ctl list >/dev/null 2>&1; then
  exec "$@"
fi
# Anything left is stale (an unclean stop, kept across `docker restart`), and
# would satisfy the wait below before the new daemon is listening.
rm -f "$SOCK"

# Its banner goes to stderr so it never pollutes the command's stdout.
"$BIN_DIR/pupptyeer" daemon >&2 &
daemon=$!

# Up to 10 seconds.
i=0
while [ ! -S "$SOCK" ]; do
  if ! kill -0 "$daemon" 2>/dev/null; then
    echo "pupptyeer-entrypoint: pupptyeer daemon exited before $SOCK appeared" >&2
    exit 1
  fi
  i=$((i + 1))
  if [ "$i" -gt 100 ]; then
    echo "pupptyeer-entrypoint: timed out waiting for $SOCK" >&2
    exit 1
  fi
  sleep 0.1
done

exec "$@"
