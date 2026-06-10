# Conformance scenario

The canonical end-to-end scenario every client must pass against a live daemon. Each client
implements it identically (`conformance/go`, `conformance/ts.mjs`, `conformance/py.py`) and
`run.sh` runs all three against one daemon. A client that diverges is a **parity break**.

Each runner uses a unique marker (`<LANG>-<nanotime>`) and `$PUPPTYEER_SOCK`.

1. **connect** to `$PUPPTYEER_SOCK`.
2. **new_session** running `cat` at 80×24 → assert a non-empty session id.
3. **attach** to the id.
4. **write_pane** `"<marker>\n"`.
5. **live output**: within 3s, the streamed output contains `<marker>`.
6. **capture_pane**: the snapshot contains `<marker>`.
7. **list_sessions**: includes the id.
8. **detach**.
9. **reattach** with a *second* connection → within 3s the **scrollback replay** contains `<marker>` (proves persistence across disconnect + replay).
10. **kill** the session.
11. within 2s, **list_sessions** no longer includes the id.
12. **gc**: `new_session` a second `cat` (`id2`), then `gc(0)` → the returned reaped list includes `id2`, and within 2s `list_sessions` no longer includes `id2`.
13. print `OK <lang>` and exit 0. Any failed assertion → print `FAIL[<lang>] …` and exit non-zero.

This exercises: server-assigned ids, attach/stream, write+echo, capture, list, detach,
persistence, reattach replay, kill+reap, and gc-by-idle - i.e. the whole verb set and the
load-bearing semantics from [PROTOCOL.md](../PROTOCOL.md).
