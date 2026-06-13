# pupptyeer-client

Thin Python client for the [pupptyeer](https://github.com/PeterSR/pupptyeer) daemon: NDJSON over a
unix socket, standard library only (no dependencies).

pupptyeer is a local daemon that owns persistent PTY sessions. This client talks to it over the
daemon's unix socket; it does not start the daemon. Install and run the daemon separately (see the
[main README](https://github.com/PeterSR/pupptyeer#readme)), or `npm i -g pupptyeer` for the
prebuilt binary.

## Install

```sh
pip install pupptyeer-client
```

Requires Python >= 3.8.

## Usage

```python
import os, sys
from pupptyeer_client import PupptyeerClient

# Connect to the daemon's unix socket (the daemon exports its path via $PUPPTYEER_SOCK).
c = PupptyeerClient.connect(os.environ["PUPPTYEER_SOCK"])

# Spawn a command in a fresh PTY; get back a session id.
sid = c.new_session(command="bash", cols=80, rows=24)

# Stream the session's live output, then attach this connection.
c.on_output(sid, lambda b: sys.stdout.buffer.write(b))
c.attach(sid, cols=80, rows=24)

# Drive it.
c.write_pane(sid, "echo hello\n")

# Read the daemon's authoritative rendered screen once the PTY goes quiet.
screen = c.capture_screen(sid, settle_ms=50)
print("\n".join(screen.lines))

c.close()  # sessions outlive the client
```

See the [protocol spec](https://github.com/PeterSR/pupptyeer/blob/main/PROTOCOL.md) for the full
verb set (`new_session`, `list_sessions`, `attach`/`detach`, `write_pane`, `capture_pane`,
`resize`, `kill`, `gc`).

## License

MIT
