// The Go client is its own module so that importing it does not drag the
// daemon's dependencies (mcp-go, creack/pty, conpty, …) into a consumer's
// build graph. It is self-contained and has zero external dependencies:
// the wire types and NDJSON codec are inlined here, mirroring the thin TS
// and Python clients. PROTOCOL.md remains the source of truth.
module github.com/PeterSR/pupptyeer/clients/go

go 1.25
