# Client parity matrix

Every client must cover the full daemon featureset ([PROTOCOL.md](../PROTOCOL.md)). Method names are
idiomatic per language; **behaviour** must be identical and is enforced by the conformance suite
([`conformance/`](../conformance)). Any new verb/behaviour lands in the daemon **and all three
clients** in the same change.

| Capability | Daemon verb | Go (`clients/go`) | TypeScript (`clients/typescript`) | Python (`clients/python`) |
|---|---|---|---|---|
| connect (or scream) | - | `client.Connect(WithSocket?,WithNamespace?)` (or `Dial(sock)`) | `PupptyeerClient.connect(sock \| {socket?,namespace?})` | `PupptyeerClient.connect(socket?,namespace?)` |
| default socket resolve | - | `DefaultSocketPath()` | `defaultSocketPath()` | `default_socket_path()` |
| connection default namespace | - | `WithNamespace(ns)` at Connect / `Namespace()` | `connect({namespace})` / `.namespace` | `connect(namespace=…)` / `.namespace` |
| per-call namespace override | `<verb>{namespace}` | trailing `ns ...string` / `InNamespace` / `WithCaptureNamespace` | `{namespace}` in each call's opts | `namespace=…` kwarg on each call |
| spawn session | `new_session` | `NewSession(cmd,args,cwd,env,cols,rows[,opts])` | `newSession({command,args,cwd,env,cols,rows,raw?,namespace?})` | `new_session(command,args,cwd,env,cols,rows,raw?,namespace?)` |
| raw session (no emulator) | `new_session{raw}` | `WithRaw()` option | `newSession({…,raw:true})` | `new_session(…,raw=True)` |
| caller-supplied id | `new_session{requested_id,get_or_create}` | `WithSessionID(id)` / `WithGetOrCreate()` options | `newSession({…,requestedId,getOrCreate})` | `new_session(…,requested_id=…,get_or_create=…)` |
| ensure (continue or create) | `new_session{requested_id,get_or_create}` | `EnsureSession(id,cmd,…)` | `ensureSession({id,command,…})` | `ensure_session(session_id,command,…)` |
| list sessions (scoped) | `list_sessions{namespace}` | `ListSessions([ns])` | `listSessions({namespace?})` | `list_sessions(namespace?)` |
| list sessions (all namespaces) | `list_sessions{all}` | `ListAllSessions()` | `listSessions({all:true})` | `list_sessions(all=True)` |
| attach (stream) | `attach` | `Attach(id,cols,rows)` | `attach(id,{cols,rows})` | `attach(id,cols,rows)` |
| detach | `detach` | `Detach(id)` | `detach(id)` | `detach(id)` |
| write (raw bytes) | `write_pane` | `WritePane(id,[]byte)` | `writeBytes(id,buf)` | `write_pane(id,bytes)` |
| write (text) | `write_pane` | `WritePane(id,[]byte(s))` | `writePane(id,text)` | `write_pane(id,str)` |
| capture buffer | `capture_pane` | `CapturePane(id[,WithSettle…])` | `capturePane(id,{settleMs?})` | `capture_pane(id,settle_ms?)` |
| render screen | `capture_pane{render}` | `CaptureScreen(id[,WithSettle…])` | `captureScreen(id,{settleMs?})` | `capture_screen(id,settle_ms?)` |
| resize | `resize` | `Resize(id,cols,rows)` | `resize(id,cols,rows)` | `resize(id,cols,rows)` |
| kill | `kill` | `Kill(id)` | `kill(id)` | `kill(id)` |
| gc (reap idle, scoped) | `gc{namespace}` | `GC(maxIdleSeconds[,ns])` | `gc(maxIdleSeconds,{namespace?})` | `gc(max_idle_seconds,namespace?)` |
| gc (reap idle, all namespaces) | `gc{all}` | `GCAll(maxIdleSeconds)` | `gc(maxIdleSeconds,{all:true})` | `gc(max_idle_seconds,all=True)` |
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
