# pupptyeer - working guide

A local daemon that owns persistent PTY sessions, with a CLI, an MCP server, and client libraries
in Go, TypeScript, and Python. Generic and Claude-agnostic - anything Claude-specific is built on
top. See [`README.md`](README.md) and the spec in [`PROTOCOL.md`](PROTOCOL.md); local design notes
live in `.agent-workspace/` (gitignored, not committed).

## The parity rule (most important)
**The daemon and all three clients move together.** Any change to the wire protocol or a verb's
behaviour lands in [`PROTOCOL.md`](PROTOCOL.md) **and** the daemon **and** all three clients **and**
the conformance scenario in the same change. A capability present in one client but missing in
another is a parity break. Constants live in `PROTOCOL.md`, never re-derived per client.

Run `/check-parity` (skill) before merging anything that touches the protocol, the daemon, or a
client. The parity matrix is [`clients/PARITY.md`](clients/PARITY.md).

Downstream of the wire protocol, the MCP tool set in `mcp/tools.go` (the `pupptyeer-mcp` binary)
mirrors the agent-facing verbs. It is **not** in the parity matrix and `conformance/run.sh` does not
exercise it, so it is the one spot the automated check misses: when you add or change a verb, update
`mcp/tools.go` by hand in the same change.

## Layout
```
cmd/pupptyeer/         binary: daemon (+ daemon install/uninstall/start/stop/status managed service) | ctl | version
mcp/                   binary: pupptyeer-mcp (MCP stdio/http front-end; own module, deps the Go client)
internal/protocol/  NDJSON Message type + codec
internal/ptyx/      cross-platform PTY (creack on Unix, ConPTY on Windows)
internal/server/    daemon: registry, connections, sessions, ring, fan-out
clients/go/         Go client (separate zero-dep module - see note below)
clients/{typescript,python}/  thin clients (built-in socket + JSON)
conformance/        scenario.md + per-language runners + run.sh (cross-client matrix)
PROTOCOL.md         canonical wire contract (source of truth)
```

## Build & test
```sh
go build -o bin/pupptyeer ./cmd/pupptyeer     # daemon + CLI
go build -C mcp -o ../bin/pupptyeer-mcp .      # pupptyeer-mcp (separate module)
go vet ./... && go test ./...           # Go unit + end-to-end drive test (run go -C mcp vet ./... too)
bash conformance/run.sh                 # cross-client conformance (Go + TS + Python)

# cross-platform compile (PTY layer is Unix + Windows)
GOOS=windows GOARCH=amd64 go build ./cmd/pupptyeer
GOOS=darwin  GOARCH=arm64 go build ./cmd/pupptyeer
```

Run a client smoke directly: `clients/typescript` → `node smoke.mjs`; `clients/python` →
`python3 smoke.py` (both honour `$PUPPTYEER_SOCK`).

## Conventions
- **Dependencies:** "zero deps" means *runtime*: the deliverable is one static binary on any
  matching arch. Good Go module deps are welcome for heavy lifting (ids via `google/uuid` and the
  cross-platform managed service via `kardianos/service` in the daemon; MCP via `mark3labs/mcp-go`
  and OAuth via `go-oidc` in the `mcp/` module); avoid deps only where genuinely unneeded (the thin
  clients).
- **The Go client is a separate module.** `clients/go` is its own module
  (`github.com/PeterSR/pupptyeer/clients/go`) with **zero external deps**, so importing it
  doesn't drag the daemon's deps (creack, conpty) into a consumer's build graph. It can't
  import `internal/protocol` across the module boundary, so it inlines its own copy of the wire
  types + NDJSON codec (`clients/go/wire.go`) - exactly like the TS/Python clients. That copy is
  bound by the parity rule: any wire change updates it too. The daemon module builds against it via
  a local `replace` in the root `go.mod`.
- **Format/vet clean:** `gofmt -l` empty and `go vet ./...` clean before commit.
- **Client versions move with the release.** Each thin client carries its version in its idiomatic
  place - `clients/typescript/package.json` `version`, `clients/python` `__version__`, `clients/go`
  `client.Version` - and all three are bumped to the new project version in the release commit (the
  daemon and `pupptyeer-mcp` are tag-driven via the `-X main.version` ldflag, so they need no manual
  bump). Keep the three in lockstep, like the parity rule for behaviour.
- **Local only (daemon):** the daemon speaks unix socket, mode 0600, and stays socket-only. The
  `pupptyeer-mcp` front-end may optionally serve Streamable HTTP with auth (none/loopback, static
  bearer token, or OAuth 2.1 resource server); that networked surface lives in the `mcp/` module,
  never the daemon.
- **No private/sibling project names in checked-in files.** Don't reference other internal projects by name in committed artifacts (code, README, PROTOCOL.md, docs, etc.). `.agent-workspace/` is gitignored scratch where that's fine.
- Keep `.agent-workspace/` decision/status docs current as design evolves (it's local-only, not committed).
