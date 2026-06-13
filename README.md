# pupptyeer

A small, local **PTY session-manager daemon**: it owns persistent pseudo-terminal sessions, lets
multiple clients (a human terminal *and* a programmatic driver) attach to the same live session,
and ships with a CLI and an MCP server to drive them. Generic and program-agnostic: anything
specific to one agent or app (TUI driving, transcript lifting, web UI) is meant to be built *on top*.

> Status: **v0, initial implementation.** Builds, tested end-to-end on Linux (Go drive test +
> TS/Python client smoke tests). macOS and Windows are experimental (see [Parity &
> platforms](#parity--platforms)). Canonical wire spec: [`PROTOCOL.md`](PROTOCOL.md).

## Why

A persistent, agent-agnostic substrate so an app can: start a long-lived process in a PTY, drive it
programmatically, stream its raw output to a browser terminal (xterm.js), and let a human and an
automated driver share the same live session. Built fresh to prioritise cross-language ergonomics
+ batteries-included over raw throughput.

## Build & run

```sh
make build        # both binaries into bin/ (or the two `go build` lines below)
make install      # install pupptyeer + pupptyeer-mcp into your go bin

go build -o bin/pupptyeer ./cmd/pupptyeer        # daemon + CLI
go build -C mcp -o ../bin/pupptyeer-mcp .         # MCP front-end (separate module)

# start the daemon in the foreground (unix socket; override path with $PUPPTYEER_SOCK)
./bin/pupptyeer daemon &

# or install it as a per-user managed service that starts at login
# (systemd --user on Linux, a launchd LaunchAgent on macOS, a Windows service)
pupptyeer daemon install                     # install + start; auto-starts at login
pupptyeer daemon status                      # not installed | installed, stopped | running
pupptyeer daemon start | stop | restart      # manage the running service
pupptyeer daemon uninstall                   # stop + remove

# drive it
./bin/pupptyeer ctl new bash                 # -> <session-id>
./bin/pupptyeer ctl new --raw bash           # raw/fast session: no terminal emulator (no rendered capture; lower CPU/latency)
./bin/pupptyeer ctl list
./bin/pupptyeer ctl send <id> $'echo hi\n'
./bin/pupptyeer ctl capture <id>             # raw scrollback bytes (ANSI included)
./bin/pupptyeer ctl capture --render <id>    # rendered visible screen (escape codes applied, one line per row)
./bin/pupptyeer ctl capture --render --settle 200 <id>  # wait for the screen to go quiet (200ms), then render
./bin/pupptyeer ctl attach <id>              # interactive: stdin forwarded, resize propagated, Ctrl-\ detaches
./bin/pupptyeer ctl attach -r <id>           # read-only output stream (auto when stdin isn't a tty)
./bin/pupptyeer ctl kill <id>
./bin/pupptyeer ctl gc --max-idle 1h         # reap sessions idle (no PTY I/O) for >= 1h; --max-idle 0 reaps all

# MCP server (separate binary; talks to the daemon over the socket).
# stdio by default:
./bin/pupptyeer-mcp

# or Streamable HTTP, with optional auth (none | token | oauth):
./bin/pupptyeer-mcp -transport http -auth none
./bin/pupptyeer-mcp -transport http -auth token -token "$PUPPTYEER_MCP_TOKEN"
./bin/pupptyeer-mcp -transport http -auth oauth \
    -oauth-issuer https://issuer.example.com -oauth-audience http://127.0.0.1:8765
```

Socket path resolution: `$PUPPTYEER_SOCK` → `$XDG_RUNTIME_DIR/pupptyeer/daemon.sock` →
`$TMPDIR/pupptyeer-<user>/daemon.sock`, where `<user>` is the numeric uid on Unix and the user SID
on Windows (so the default dir is per-user on every platform). Local only: on Unix the socket dir is
mode `0700` and the socket `0600`; on Windows, where mode bits are ignored, the daemon installs a
DACL restricting the socket dir to the current user.

`pupptyeer daemon install` registers the daemon as a **per-user** service (via
[`kardianos/service`](https://github.com/kardianos/service): systemd `--user`, a launchd LaunchAgent,
or a Windows service) that starts at login and restarts with exponential backoff if it exits, so the
socket is always there for the CLI, the MCP server, and the language clients. It runs as you (not
root), keeping the per-user, local-only socket model intact. Any `PUPPTYEER_SOCK` / `PUPPTYEER_CONFIG`
set in the shell at install time is baked into the service environment so the service and your CLI
resolve the same socket; change either and re-run `install`. Linux is verified (on systemd the
install also wires the unit to `default.target` so it starts at login, and adds a restart-backoff
drop-in capped at five minutes); macOS (LaunchAgent) and Windows (service) ride the same command but
are experimental, like the rest of the cross-platform surface.

## Configuration (optional)

The CLI reads an optional [TOML](https://toml.io) config file, resolved in order: `$PUPPTYEER_CONFIG`,
else `$XDG_CONFIG_HOME/pupptyeer/config.toml` (honoured on every platform), else the OS-native user
config dir (`~/.config` on Linux, `~/Library/Application Support` on macOS, `%AppData%` on Windows).
It is purely client-side, so it affects only `ctl`; the daemon and wire protocol are untouched. A
missing file is fine; a malformed one (or an unknown key) is reported with the path and line.
Standard TOML applies: string values are quoted, and inline `#` comments are allowed.

```toml
# detach key for interactive attach (default: ctrl-\, like dtach).
# accepts ctrl-X / c-X / ^X, a hex byte like 0x1c, or "none" to disable
# (leaving SIGINT/SIGTERM as the only way out). use single quotes so the
# trailing backslash is taken literally.
detach_key = 'ctrl-]'

# optional tmux-style prefix: detach becomes a two-key sequence,
# detach_prefix then detach_key. uncomment both for "Ctrl-b then d" like
# tmux. the prefix must be a control key; with one set detach_key may be
# any single key (e.g. d). the prefix is held and only forwarded to the
# session if the next key isn't detach_key, so a prefix you also use
# inside the session still works.
#   detach_prefix = 'ctrl-b'
#   detach_key = 'd'

# default size for `ctl new` when no explicit size is given (default 80x24)
default_cols = 120
default_rows = 40

# suppress the "[pupptyeer: attached ...]" banner on attach (default false)
quiet = false
```

## Clients

- **Go**: `clients/go` (its own zero-dependency module: `import client "github.com/PeterSR/pupptyeer/clients/go"`)
- **TypeScript / Node**: `npm i pupptyeer-client` (source in `clients/typescript`; zero deps)
- **Python**: `pip install pupptyeer-client` (source in `clients/python`; stdlib only)

Prefer not to build the Go binary yourself? `npm i -g pupptyeer` installs the prebuilt daemon, CLI,
and MCP front-end for your platform. See [`PUBLISHING.md`](PUBLISHING.md) for how the packages are
released.

## Protocol

NDJSON over a unix socket: one JSON object per line, raw PTY bytes base64 in `data`. Verbs:
`new_session`, `list_sessions`, `attach`/`detach`, `write_pane`, `capture_pane`, `resize`, `kill`,
`gc` (reap sessions idle past a threshold). Full spec in [`PROTOCOL.md`](PROTOCOL.md).

`capture_pane` has two modes. By default it returns raw scrollback bytes. With `render`, the daemon
maintains one live terminal emulator per session and returns the **rendered visible screen** - the
authoritative grid of `cols`×`rows`, the cursor, and whether the program is on the alternate screen
buffer - so clients never have to embed their own emulator to read a TUI. Either mode accepts
`settle_ms` to hold the reply until the PTY has been quiet for that long (the reliable way to read a
screen after sending input). Rendering reports *what is on the screen*, never what it means; any
interpretation belongs in the layer above.

## Fast path (opt-in)

The defaults optimise for cross-language ergonomics (one JSON shape, three clients in lockstep) over
raw speed, and run a terminal emulator per session to power rendered capture. Two opt-ins shed that
overhead for throughput- or latency-sensitive consumers, leaving the default protocol untouched:

- **Raw sessions** - `new_session` with `raw:true` (or `ctl new --raw`) run no terminal emulator:
  lower CPU and latency, at the cost of rendered capture (raw scrollback capture still works).
- **Raw firehose** - an optional second socket at `<sock>.raw`: a transparent, unframed byte pipe to
  one session's PTY (no base64, no JSON, no framing). Reachable from any language, or from
  `socat`/`nc`, with no client library; the Go client wraps it as `AttachRaw`. It is out of band and
  out of the parity matrix (like the MCP tools), so the default socket and protocol are unchanged.

Combined, the fast path streams raw bytes at parity with a lean binary PTY transport (measured around
320 MiB/s and a ~30µs echo round-trip on Linux), while the default NDJSON path keeps the rich
features. See [`PROTOCOL.md`](PROTOCOL.md) for the handshake.

## MCP server

`pupptyeer-mcp` is a separate binary and Go module that exposes the daemon's verbs as MCP tools. It
depends only on the Go client and reaches the daemon over the socket, so the daemon never inherits
the MCP, HTTP, or OAuth dependencies. Its `read_screen` tool returns the rendered visible grid by
default (set `render=false` for raw scrollback) and accepts `settle_ms`, so an agent can send input
and then read a settled screen. It serves **stdio** (the default) or **Streamable HTTP**. The
HTTP transport offers three auth modes: loopback-only with no token, a static bearer token
(`-auth token`), or an OAuth 2.1 resource server (`-auth oauth`) that validates an external
identity provider's bearer JWTs and publishes RFC 9728 protected-resource metadata.

## Layout

```
cmd/pupptyeer            daemon | ctl | version
mcp/                     pupptyeer-mcp: MCP stdio/http front-end (separate module; deps the Go client)
internal/protocol     NDJSON message type + codec
internal/ptyx         cross-platform PTY (creack on Unix, ConPTY on Windows)
internal/server       daemon: registry, connections, sessions, ring buffer, fan-out
clients/go            Go client library (separate zero-dep module)
clients/{typescript,python}   thin clients      (parity matrix: clients/PARITY.md)
conformance/          scenario + per-language runners + run.sh (cross-client matrix)
PROTOCOL.md           canonical wire contract (source of truth)
CLAUDE.md             working guide + parity rule
```

## Parity & platforms

The daemon and the Go/TS/Python clients move together: `bash conformance/run.sh` runs the same
scenario through all three against one daemon (the `/check-parity` skill bundles this). The PTY
layer is cross-platform (creack on Unix, ConPTY on Windows); `windows/amd64`, `windows/arm64`, and
`darwin/arm64` cross-compile cleanly.

**Linux is the tested, supported platform. macOS and Windows are both experimental**: they
cross-compile cleanly but have not been run on real hardware. macOS rides the same Unix path
(creack, SIGWINCH, `0700`/`0600` socket perms), so it is expected to work but is unverified. Windows
has its platform-specific pieces in place (ConPTY, polled resize, per-user socket dir + DACL), plus
extra caveats: `SIGTERM` is not deliverable (use Ctrl-C / `SIGINT`), ANSI rendering assumes a
VT-capable console (Windows Terminal, or `ENABLE_VIRTUAL_TERMINAL_PROCESSING`), and the socket-dir
DACL fails closed (the daemon refuses to start if it cannot be applied). Verify on real hardware
before relying on either.

## Related projects

pupptyeer is a PTY session manager in the tmux/screen lineage, built MCP-native from day one, on
the bet that machine-driven terminals are here to stay, so a protocol for them belongs in the core
rather than bolted on later. It stays general-purpose: a human shell, a script in any of the three
client languages, or an AI tool over MCP are all first-class ways to drive the same session. That
places it between two camps, and depending on what you're after, something in either may serve you
better:

**Classic session managers** (general-purpose, predate MCP):

- [zmux](https://github.com/smithersai/zmux): daemon-owned PTYs with attach/detach over unix
  JSON-RPC and server-side scrollback. tmux-like window/pane model. Zig; macOS + Linux; no MCP.
- tmux / screen: the originals; battle-tested multiplexers, but no machine-native protocol.

**MCP-first, agent-focused tools** (MCP-native, built around AI agents):

- [Forge](https://forgemcp.dev/): persistent `node-pty` daemon with a built-in MCP server, ring
  buffer with incremental reads, multi-agent fan-out, and a browser dashboard. Node, over HTTP.
- [pty-mcp + ai-tmux](https://glama.ai/mcp/servers/raychao-oao/pty-mcp): MCP server backed by an
  `ai-tmux` daemon that keeps PTYs alive over a unix socket. Single Go binary; macOS + Linux,
  Windows via WSL2.

If AI agents are front and center for you (orchestrating fleets of them, spawning sub-agents,
watching them in a dashboard), the agent-focused tools above are the better bet; that is what they
are built for. In pupptyeer an agent is simply one first-class client among several: the MCP server
sits beside the CLI and the language clients, none privileged over the others.

pupptyeer's own niche is the diagonal between the two: a generic, agent-agnostic session manager
over a local unix socket, with a built-in MCP server **and** a CLI **and** Go, TypeScript, and
Python clients held in lockstep protocol parity. If you want one session substrate that is equally
at home behind a shell, a script, and an AI tool, that is the gap it fills.

## Tests

```sh
go test ./...     # drive test (attach/detach/replay), resize arbitration, protocol round-trip
```

## License

MIT.
