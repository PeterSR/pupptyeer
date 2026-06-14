# Client parity matrix

Every client must cover the full daemon featureset ([PROTOCOL.md](../PROTOCOL.md)). Method names are
idiomatic per language; **behaviour** must be identical and is enforced by the conformance suite
([`conformance/`](../conformance)). Any new verb/behaviour lands in the daemon **and all three
clients** in the same change.

| Capability | Daemon verb | Go (`clients/go`) | TypeScript (`clients/typescript`) | Python (`clients/python`) |
|---|---|---|---|---|
| connect | - | `client.Dial(sock)` | `PupptyeerClient.connect(sock)` | `PupptyeerClient.connect(sock)` |
| spawn session | `new_session` | `NewSession(cmd,args,cwd,env,cols,rows[,opts])` | `newSession({command,args,cwd,env,cols,rows,raw?})` | `new_session(command,args,cwd,env,cols,rows,raw?)` |
| raw session (no emulator) | `new_session{raw}` | `WithRaw()` option | `newSession({…,raw:true})` | `new_session(…,raw=True)` |
| caller-supplied id | `new_session{requested_id,get_or_create}` | `WithSessionID(id)` / `WithGetOrCreate()` options | `newSession({…,requestedId,getOrCreate})` | `new_session(…,requested_id=…,get_or_create=…)` |
| ensure (continue or create) | `new_session{requested_id,get_or_create}` | `EnsureSession(id,cmd,…)` | `ensureSession({id,command,…})` | `ensure_session(session_id,command,…)` |
| list sessions | `list_sessions` | `ListSessions()` | `listSessions()` | `list_sessions()` |
| attach (stream) | `attach` | `Attach(id,cols,rows)` | `attach(id,{cols,rows})` | `attach(id,cols,rows)` |
| detach | `detach` | `Detach(id)` | `detach(id)` | `detach(id)` |
| write (raw bytes) | `write_pane` | `WritePane(id,[]byte)` | `writeBytes(id,buf)` | `write_pane(id,bytes)` |
| write (text) | `write_pane` | `WritePane(id,[]byte(s))` | `writePane(id,text)` | `write_pane(id,str)` |
| capture buffer | `capture_pane` | `CapturePane(id[,WithSettle…])` | `capturePane(id,{settleMs?})` | `capture_pane(id,settle_ms?)` |
| render screen | `capture_pane{render}` | `CaptureScreen(id[,WithSettle…])` | `captureScreen(id,{settleMs?})` | `capture_screen(id,settle_ms?)` |
| resize | `resize` | `Resize(id,cols,rows)` | `resize(id,cols,rows)` | `resize(id,cols,rows)` |
| kill | `kill` | `Kill(id)` | `kill(id)` | `kill(id)` |
| gc (reap idle) | `gc` | `GC(maxIdleSeconds)` | `gc(maxIdleSeconds)` | `gc(max_idle_seconds)` |
| live output cb | `output` | `Events()` channel | `onOutput(id,fn)` | `on_output(id,fn)` |
| all events cb | * | `Events()` channel | `onEvent(fn)` | `on_event(fn)` |
| close | - | `Close()` | `close()` | `close()` |

## Rules
- **Behaviour parity is the contract; naming is idiomatic.** A capability missing in one client is a parity break.
- **Constants come from [PROTOCOL.md](../PROTOCOL.md)** - never re-derive per client.
- **Empty session list → `[]`**, never `null`, at every client surface.
- Run [`/check-parity`](../.claude/skills/check-parity/SKILL.md) (or `conformance/run.sh`) before merging any change that touches a client or the protocol.

## Out of the matrix (optional extensions)
- **Raw firehose** (`<sock>.raw`, see [PROTOCOL.md](../PROTOCOL.md)) is an optional out-of-band fast
  path, **not** a parity requirement - like `mcp/tools.go`, it is exempt from this matrix and the
  conformance suite. The daemon implements it; clients add it only where wanted. Today only the Go
  client exposes it (`AttachRaw(id) net.Conn`); its absence in TypeScript/Python is **not** a parity
  break. The in-band `raw:true` session flag above, by contrast, **is** in the matrix.
