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
12. **rendered capture + settle**: `new_session` running `sh -c "printf 'A\033[1;10HB'; sleep 2"` at 80×24 (`id3`). This prints `A` at column 1, moves the cursor to row 1 column 10, then prints `B` - so a naive ANSI strip would collapse it to `AB`. Call the **rendered** capture with a **settle** of 200ms (timeout 2000ms). Assert the grid is 80×24 with 24 lines and `lines[0]` is `A` + 8 spaces + `B` (i.e. `B` at index 9), proving the daemon applied the cursor move instead of stripping it. Then **kill** `id3`.
13. **gc**: `new_session` a second `cat` (`id2`), then `gc(0)` → the returned reaped list includes `id2`, and within 2s `list_sessions` no longer includes `id2`.
14. print `OK <lang>` and exit 0. Any failed assertion → print `FAIL[<lang>] …` and exit non-zero.

This exercises: server-assigned ids, attach/stream, write+echo, capture, list, detach,
persistence, reattach replay, kill+reap, rendered capture + settle, and gc-by-idle - i.e. the
whole verb set and the load-bearing semantics from [PROTOCOL.md](../PROTOCOL.md).
