# Client parity matrix

Every client must cover the full daemon featureset ([PROTOCOL.md](../PROTOCOL.md)). Method names are
idiomatic per language; **behaviour** must be identical and is enforced by the conformance suite
([`conformance/`](../conformance)). Any new verb/behaviour lands in the daemon **and all three
clients** in the same change.

| Capability | Daemon verb | Go (`clients/go`) | TypeScript (`clients/typescript`) | Python (`clients/python`) |
|---|---|---|---|---|
| connect | - | `client.Dial(sock)` | `PupptyeerClient.connect(sock)` | `PupptyeerClient.connect(sock)` |
| spawn session | `new_session` | `NewSession(cmd,args,cwd,env,cols,rows)` | `newSession({command,args,cwd,env,cols,rows})` | `new_session(command,args,cwd,env,cols,rows)` |
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
