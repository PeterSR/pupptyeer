#!/usr/bin/env python3
"""Conformance runner (Python) - implements conformance/scenario.md."""
import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "clients", "python"))
from pupptyeer_client import PupptyeerClient  # noqa: E402


def fail(msg):
    print("FAIL[python] " + msg, file=sys.stderr)
    sys.exit(1)


sock = os.environ.get("PUPPTYEER_SOCK")
if not sock:
    fail("PUPPTYEER_SOCK not set")

marker = ("PY-%d" % time.time_ns()).encode()

c = PupptyeerClient.connect(sock)
sid = c.new_session(command="cat", cols=80, rows=24)
if not sid:
    fail("empty session id")

acc = bytearray()
c.on_output(sid, lambda b: acc.extend(b))
c.attach(sid, cols=80, rows=24)
c.write_pane(sid, marker + b"\n")

deadline = time.time() + 3.0
while time.time() < deadline and marker not in bytes(acc):
    time.sleep(0.04)
if marker not in bytes(acc):
    fail("marker not in live output")

if marker not in c.capture_pane(sid):
    fail("capture missing marker")

if not any(s["id"] == sid for s in c.list_sessions()):
    fail("session not listed")

c.detach(sid)

# reattach with a second connection → scrollback replay
b = PupptyeerClient.connect(sock)
racc = bytearray()
b.on_output(sid, lambda x: racc.extend(x))
b.attach(sid, cols=80, rows=24)
deadline = time.time() + 3.0
while time.time() < deadline and marker not in bytes(racc):
    time.sleep(0.04)
if marker not in bytes(racc):
    fail("scrollback replay missing marker")

b.kill(sid)
deadline = time.time() + 2.0
gone = False
while time.time() < deadline:
    if not any(s["id"] == sid for s in c.list_sessions()):
        gone = True
        break
    time.sleep(0.04)
if not gone:
    fail("session still listed after kill")

# gc: a fresh session reaped by gc(0) (reap all idle sessions).
sid2 = c.new_session(command="cat", cols=80, rows=24)
reaped = c.gc(0)
if not any(s["id"] == sid2 for s in reaped):
    fail("gc did not report reaping the session")
deadline = time.time() + 2.0
gone = False
while time.time() < deadline:
    if not any(s["id"] == sid2 for s in c.list_sessions()):
        gone = True
        break
    time.sleep(0.04)
if not gone:
    fail("session still listed after gc")

c.close()
b.close()
print("OK python")
