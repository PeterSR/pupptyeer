#!/usr/bin/env python3
"""Smoke test for the Python client. Requires a running daemon.

    PUPPTYEER_SOCK=/tmp/pupptyeer-e2e.sock python3 smoke.py
"""
import os
import sys
import time

from pupptyeer_client import PupptyeerClient

sock = os.environ.get("PUPPTYEER_SOCK", "/tmp/pupptyeer-e2e.sock")
c = PupptyeerClient.connect(sock)

marker = "PY-MARKER-%d" % time.time_ns()
sid = c.new_session(command="cat", cols=80, rows=24)

acc = bytearray()
c.on_output(sid, lambda b: acc.extend(b))
c.attach(sid, cols=80, rows=24)
c.write_pane(sid, marker + "\n")

deadline = time.time() + 3.0
while time.time() < deadline and marker.encode() not in bytes(acc):
    time.sleep(0.05)
if marker.encode() not in bytes(acc):
    print("FAIL: marker not seen in live output", file=sys.stderr)
    sys.exit(1)

cap = c.capture_pane(sid)
if marker.encode() not in cap:
    print("FAIL: capture missing marker", file=sys.stderr)
    sys.exit(1)

if not any(s["id"] == sid for s in c.list_sessions()):
    print("FAIL: session not listed", file=sys.stderr)
    sys.exit(1)

c.kill(sid)
c.close()
print("Python client smoke: OK (marker seen live + in capture, listed, killed)")
