"""Thin Python client for the pupptyeer daemon (NDJSON over a unix
socket). Standard library only.

    from pupptyeer_client import PupptyeerClient
    c = PupptyeerClient.connect(os.environ["PUPPTYEER_SOCK"])
    sid = c.new_session(command="bash")
    c.on_output(sid, lambda b: sys.stdout.buffer.write(b))
    c.attach(sid, cols=80, rows=24)
    c.write_pane(sid, "echo hi\\n")
"""
from __future__ import annotations

import base64
import json
import socket
import threading
from dataclasses import dataclass, field
from typing import Callable, List, Optional


@dataclass
class Cursor:
    """Cursor position in a rendered capture; 0-based, col may equal cols."""
    row: int = 0
    col: int = 0
    visible: bool = True


@dataclass
class Screen:
    """The rendered visible terminal grid returned by capture_screen.
    lines holds exactly rows strings, each space-padded to cols."""
    cols: int = 0
    rows: int = 0
    lines: List[str] = field(default_factory=list)
    cursor: Cursor = field(default_factory=Cursor)
    alt_screen: bool = False


class PupptyeerClient:
    def __init__(self, sock: socket.socket):
        self._sock = sock
        self._lock = threading.Lock()
        self._next_id = 0
        self._pending: dict[int, list] = {}  # id -> [Event, result]
        self._output_handlers: dict[str, list[Callable[[bytes], None]]] = {}
        self._event_handlers: list[Callable[[dict], None]] = []
        self._closed = False
        self._reader = threading.Thread(target=self._read_loop, daemon=True)
        self._reader.start()

    @classmethod
    def connect(cls, path: str) -> "PupptyeerClient":
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        s.connect(path)
        return cls(s)

    def _read_loop(self) -> None:
        buf = b""
        f = self._sock
        while True:
            try:
                chunk = f.recv(65536)
            except OSError:
                break
            if not chunk:
                break
            buf += chunk
            while b"\n" in buf:
                line, buf = buf.split(b"\n", 1)
                if not line:
                    continue
                try:
                    msg = json.loads(line)
                except json.JSONDecodeError:
                    continue
                self._route(msg)
        self._closed = True
        with self._lock:
            for slot in self._pending.values():
                slot[1] = {"type": "error", "message": "connection closed"}
                slot[0].set()
            self._pending.clear()

    def _route(self, msg: dict) -> None:
        mid = msg.get("id")
        if mid:
            with self._lock:
                slot = self._pending.pop(mid, None)
            if slot is not None:
                slot[1] = msg
                slot[0].set()
                return
        if msg.get("type") == "output":
            hs = self._output_handlers.get(msg.get("session", ""))
            if hs:
                data = base64.b64decode(msg.get("data", "") or "")
                # Copy so a handler that unsubscribes mid-dispatch is safe.
                for fn in list(hs):
                    fn(data)
        for fn in list(self._event_handlers):
            fn(msg)

    def _send(self, msg: dict) -> None:
        self._sock.sendall((json.dumps(msg) + "\n").encode())

    def _call(self, msg: dict, timeout: float = 5.0) -> dict:
        ev = threading.Event()
        slot = [ev, None]  # _route fills slot[1] then sets ev
        with self._lock:
            self._next_id += 1
            mid = self._next_id
            self._pending[mid] = slot
        msg["id"] = mid
        self._send(msg)
        if not ev.wait(timeout):
            raise TimeoutError("no reply for id %d" % mid)
        reply = slot[1] or {}
        if reply.get("type") == "error":
            raise RuntimeError(reply.get("message", "error"))
        return reply

    def on_output(self, session: str, fn: Callable[[bytes], None]) -> Callable[[], None]:
        """Register fn for a session's live output. Multiple handlers per
        session are supported (they all fire). Returns a callable that
        unsubscribes this handler."""
        hs = self._output_handlers.setdefault(session, [])
        hs.append(fn)

        def off() -> None:
            cur = self._output_handlers.get(session)
            if cur is None:
                return
            try:
                cur.remove(fn)
            except ValueError:
                return
            if not cur:
                self._output_handlers.pop(session, None)

        return off

    def on_event(self, fn: Callable[[dict], None]) -> Callable[[], None]:
        """Register fn for all unsolicited messages. Returns a callable
        that unsubscribes this handler."""
        self._event_handlers.append(fn)

        def off() -> None:
            try:
                self._event_handlers.remove(fn)
            except ValueError:
                pass

        return off

    def new_session(self, command: str, args=None, cwd: str = "", env=None,
                    cols: int = 80, rows: int = 24) -> str:
        r = self._call({"type": "new_session", "command": command,
                         "args": args or [], "cwd": cwd, "env": env,
                         "cols": cols, "rows": rows})
        return r.get("session", "")

    def list_sessions(self) -> list:
        return self._call({"type": "list_sessions"}).get("sessions") or []

    def attach(self, session: str, cols: int = 0, rows: int = 0) -> None:
        self._call({"type": "attach", "session": session, "cols": cols, "rows": rows})

    def detach(self, session: str) -> None:
        self._send({"type": "detach", "session": session})

    def write_pane(self, session: str, text) -> None:
        data = text.encode() if isinstance(text, str) else bytes(text)
        self._send({"type": "write_pane", "session": session,
                    "data": base64.b64encode(data).decode()})

    def capture_pane(self, session: str, settle_ms: int = 0,
                     timeout_ms: int = 0) -> bytes:
        """Return the session's raw scrollback bytes. With settle_ms > 0,
        first waits for the screen to go quiet."""
        r = self._call({"type": "capture_pane", "session": session,
                        "settle_ms": settle_ms, "timeout_ms": timeout_ms})
        return base64.b64decode(r.get("data", "") or "")

    def capture_screen(self, session: str, settle_ms: int = 0,
                       timeout_ms: int = 0) -> Screen:
        """Return the daemon's authoritative rendered screen (the visible
        grid, not scrollback). With settle_ms > 0, first waits for the
        screen to go quiet - the usual way to read a TUI after sending
        input."""
        r = self._call({"type": "capture_pane", "session": session,
                        "render": True, "settle_ms": settle_ms,
                        "timeout_ms": timeout_ms})
        c = r.get("cursor") or {}
        return Screen(
            cols=r.get("cols", 0),
            rows=r.get("rows", 0),
            lines=r.get("lines") or [],
            cursor=Cursor(row=c.get("row", 0), col=c.get("col", 0),
                          visible=c.get("visible", True)),
            alt_screen=bool(r.get("alt_screen", False)),
        )

    def resize(self, session: str, cols: int, rows: int) -> None:
        self._send({"type": "resize", "session": session, "cols": cols, "rows": rows})

    def kill(self, session: str) -> None:
        self._call({"type": "kill", "session": session})

    def gc(self, max_idle_seconds: int) -> list:
        """Reap sessions idle (no PTY input/output) for >= max_idle_seconds;
        returns the reaped SessionInfo dicts. max_idle_seconds <= 0 reaps all."""
        return self._call({"type": "gc",
                            "max_idle_seconds": max_idle_seconds}).get("sessions") or []

    def close(self) -> None:
        try:
            self._sock.close()
        except OSError:
            pass
