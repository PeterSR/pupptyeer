# pupptyeer-client

Thin Node client for the [pupptyeer](https://github.com/PeterSR/pupptyeer) daemon: NDJSON over a
unix socket, zero runtime dependencies, ships as plain ESM with hand-written `.d.ts` (no build step).

pupptyeer is a local daemon that owns persistent PTY sessions. This client talks to it over the
daemon's unix socket; it does not start the daemon. Install and run the daemon separately (see the
[main README](https://github.com/PeterSR/pupptyeer#readme)), or `npm i -g pupptyeer` for the
prebuilt binary.

## Install

```sh
npm i pupptyeer-client
```

Requires Node >= 18.

## Usage

```js
import { PupptyeerClient } from "pupptyeer-client";

// Connect to the daemon's unix socket (the daemon exports its path via $PUPPTYEER_SOCK).
const c = await PupptyeerClient.connect(process.env.PUPPTYEER_SOCK);

// Spawn a command in a fresh PTY; get back a session id.
const sid = await c.newSession({ command: "bash", cols: 80, rows: 24 });

// Stream the session's live output.
c.onOutput(sid, (bytes) => process.stdout.write(bytes));
await c.attach(sid, { cols: 80, rows: 24 });

// Drive it.
c.writePane(sid, "echo hello\n");

// Read the daemon's authoritative rendered screen once the PTY goes quiet.
const screen = await c.captureScreen(sid, { settleMs: 50 });
console.log(screen.lines.join("\n"));

c.close(); // sessions outlive the client
```

See the [protocol spec](https://github.com/PeterSR/pupptyeer/blob/main/PROTOCOL.md) for the full
verb set (`new_session`, `list_sessions`, `attach`/`detach`, `write_pane`, `capture_pane`,
`resize`, `kill`, `gc`) and `index.d.ts` for the complete typed surface.

## License

MIT
