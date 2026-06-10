---
name: check-parity
description: Verify the Go/TS/Python clients are in feature + behaviour parity with each other and the daemon. Use after changing the protocol, the daemon, or any client, and before merging such a change.
---

# check-parity

Confirm the daemon and all three clients agree on the featureset and behaviour defined in
[`PROTOCOL.md`](../../../PROTOCOL.md) and [`clients/PARITY.md`](../../../clients/PARITY.md).

Run these and report a concise PASS/FAIL table:

1. **Go unit + drive tests**
   ```sh
   go vet ./... && go test ./...
   ```

2. **Cross-client conformance** (builds the daemon, runs the same scenario through Go, TS, Python):
   ```sh
   bash conformance/run.sh
   ```
   A `FAIL` for any client is a **parity break**, never a flake - investigate before merging.

3. **Cross-platform compile** (the PTY layer has Unix + Windows backends):
   ```sh
   GOOS=windows GOARCH=amd64 go build ./cmd/pupptyeer
   GOOS=darwin  GOARCH=arm64 go build ./cmd/pupptyeer
   ```

4. **API-surface audit** - open [`clients/PARITY.md`](../../../clients/PARITY.md) and confirm every
   row exists in `clients/go`, `clients/typescript/index.mjs`, and `clients/python/pupptyeer_client.py`.
   A capability present in one client but missing in another is a parity break. If the protocol
   gained a verb, it must appear in the daemon, all three clients, the parity matrix, and the
   conformance scenario.

5. **MCP tool audit** (the gap conformance cannot see) - if a verb was added or changed and it is
   agent-facing, confirm the MCP tool set in [`mcp/tools.go`](../../../mcp/tools.go) (the
   `pupptyeer-mcp` binary) was updated to match. `conformance/run.sh` does not exercise the MCP
   front-end, so this is a hand check.

Report: per-step PASS/FAIL, and for any failure the specific client/step and the assertion that
broke. Do not declare parity green unless steps 1–5 all pass.
