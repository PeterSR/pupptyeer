# pupptyeer protocol (v0)

This is the **canonical contract** every client and the daemon must implement. Behaviour changes
land here first, then in all clients in the same change (see [CLAUDE.md](CLAUDE.md) parity rule).

## Transport
- **Unix domain socket**, local only, mode `0600`.
- Path resolution: `$PUPPTYEER_SOCK` → `$XDG_RUNTIME_DIR/pupptyeer/daemon.sock` → `$TMPDIR/pupptyeer-<uid>/daemon.sock`.

## Framing - NDJSON
One JSON object per line, terminated by `\n` (a.k.a. JSON Lines). Raw PTY bytes are carried
base64-encoded in the `data` field (so newlines/control bytes never break framing).

## Constants (identical in every implementation)
| name | value |
|---|---|
| PTY read chunk | 32 KiB |
| scrollback ring | 256 KiB |
| default size | 80 × 24 |
| outbound queue (server, per conn) | 256 messages |
| decoder max line | 1 MiB |
| capture settle default timeout | 5 s |

## Messages
A single object shape; `type` discriminates. `id` (int, >0) correlates a request with its reply.

### Client → Server
| type | fields | reply |
|---|---|---|
| `new_session` | `command`, `args?`, `cwd?`, `env?`, `cols`, `rows` | `ok{session}` |
| `list_sessions` | - | `sessions{sessions[]}` |
| `attach` | `session`, `cols?`, `rows?` | `attached`, then `output…`, then `scrollback_end` |
| `detach` | `session` | - |
| `write_pane` | `session`, `data`(base64) **or** `text` | - (error on failure) |
| `capture_pane` | `session`, `render?`, `settle_ms?`, `timeout_ms?` | `capture{data}` or, with `render`, `capture{cols,rows,lines[],cursor,alt_screen}` |
| `resize` | `session`, `cols`, `rows` | - |
| `kill` | `session` | `ok` |
| `gc` | `max_idle_seconds` | `reaped{sessions[]}` |

### Server → Client
`ok{session?}` · `error{message}` · `sessions{sessions[]}` · `attached` · `output{data}` ·
`scrollback_end` · `capture{data | cols,rows,lines[],cursor,alt_screen}` · `exit{exit_code}` ·
`session_closed` · `reaped{sessions[]}`

`SessionInfo`: `{ id, command, args?, cwd?, cols, rows, created(RFC3339), last_activity(RFC3339), attached, alive }`.
`Cursor` (rendered capture): `{ row, col, visible }`, 0-based; `row` in `[0,rows)`, `col` in `[0,cols]` (`col == cols` is a pending-wrap cursor).
`last_activity` is the time of the most recent PTY input or output (initialised to `created`).
An empty session list is normalised to `[]` (never `null`) at the client surface - this applies to
both `sessions` and `reaped`.

## Semantics (the behaviours conformance pins down)
1. **Server-assigned ids** - UUIDs minted by the daemon.
2. **Persist across disconnect** - a session lives until the child exits or an explicit `kill`; a client disconnect detaches but never kills.
3. **Multi-client fan-out** - every attached connection receives the same `output` bytes; input from any client merges into the PTY.
4. **Scrollback replay** - `attach` replays the ring as `output` then sends `scrollback_end`.
5. **resize = smallest** - effective PTY size is the min cols/rows across attached clients; none attached ⇒ last size retained. The daemon only sets the winsize; the program inside wraps to it.
6. **Backpressure** - a client whose outbound queue fills is dropped (detached), never blocking the PTY or other clients.
7. **gc by idle** - `gc` kills every session whose idle time (now − `last_activity`) is **≥** `max_idle_seconds` and returns their `SessionInfo` (snapshotted just before the kill). Idle counts only PTY input/output; attaching or detaching does **not** reset it. `max_idle_seconds` of `0` reaps every session. Reaped sessions emit the usual `session_closed` to any attached clients and leave the registry like a `kill`.
8. **rendered capture** - `capture_pane` with `render:true` returns the *visible screen* (not scrollback) as the daemon's authoritative terminal grid: `cols`×`rows` (the effective size), `lines[]` (exactly `rows` strings, each padded with spaces to `cols`, no escape sequences), `cursor{row,col,visible}`, and `alt_screen` (true when the program is on the alternate screen buffer). The daemon maintains one live emulator per session, fed the same bytes as the ring, so the grid is correct regardless of ring size. The raw `data` field is **omitted** when `render` is set; `render` omitted/false returns raw scrollback bytes in `data` exactly as before. Rendering carries **no interpretation** - it answers "what is on the screen", never "what it means".
9. **settle read** - `settle_ms > 0` makes `capture_pane` hold its reply until the PTY has produced no output for a continuous `settle_ms` window (snapshot taken once quiet), bounded by `timeout_ms` total wait (≤ 0 ⇒ the *capture settle default timeout*). `settle_ms ≤ 0` snapshots immediately. Applies to both raw and rendered capture; it only delays this one reply and never blocks the PTY or other connections.

## Conformance
The canonical scenario every client must pass against a live daemon lives in
[`conformance/scenario.md`](conformance/scenario.md); run all clients with
[`conformance/run.sh`](conformance/run.sh).
