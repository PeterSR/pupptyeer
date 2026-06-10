// Thin Node client for the pupptyeer daemon (NDJSON over a unix
// socket). Zero dependencies - uses the built-in `net` module.
//
//   import { PupptyeerClient } from "./index.mjs";
//   const c = await PupptyeerClient.connect(process.env.PUPPTYEER_SOCK);
//   const id = await c.newSession({ command: "bash" });
//   c.onOutput(id, (bytes) => process.stdout.write(bytes));
//   await c.attach(id, { cols: 80, rows: 24 });
//   await c.writePane(id, "echo hi\n");

import net from "node:net";

export class PupptyeerClient {
  constructor(socket) {
    this.sock = socket;
    this.nextId = 0;
    this.pending = new Map(); // id -> {resolve, reject}
    this.outputHandlers = new Map(); // session -> Set<fn(Buffer)>
    this.eventHandlers = new Set(); // Set<fn(msg)>
    this._buf = "";
    socket.on("data", (chunk) => this._onData(chunk));
    socket.on("close", () => {
      for (const { reject } of this.pending.values()) reject(new Error("connection closed"));
      this.pending.clear();
    });
  }

  static connect(path) {
    return new Promise((resolve, reject) => {
      const socket = net.createConnection({ path }, () => resolve(new PupptyeerClient(socket)));
      socket.once("error", reject);
    });
  }

  _onData(chunk) {
    this._buf += chunk.toString("utf8");
    let nl;
    while ((nl = this._buf.indexOf("\n")) >= 0) {
      const line = this._buf.slice(0, nl);
      this._buf = this._buf.slice(nl + 1);
      if (!line) continue;
      let msg;
      try { msg = JSON.parse(line); } catch { continue; }
      this._route(msg);
    }
  }

  _route(msg) {
    if (msg.id && this.pending.has(msg.id)) {
      const { resolve, reject } = this.pending.get(msg.id);
      this.pending.delete(msg.id);
      if (msg.type === "error") reject(new Error(msg.message));
      else resolve(msg);
      return;
    }
    if (msg.type === "output") {
      const hs = this.outputHandlers.get(msg.session);
      if (hs && hs.size) {
        const bytes = Buffer.from(msg.data || "", "base64");
        // Snapshot so a handler that unsubscribes mid-dispatch is safe.
        for (const fn of [...hs]) fn(bytes);
      }
    }
    for (const fn of [...this.eventHandlers]) fn(msg);
  }

  _send(msg) { this.sock.write(JSON.stringify(msg) + "\n"); }

  _call(msg) {
    const id = ++this.nextId;
    msg.id = id;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this._send(msg);
    });
  }

  // onOutput registers fn for a session's live output. Multiple handlers
  // per session are supported (they all fire); returns an unsubscribe
  // function that removes this handler.
  onOutput(session, fn) {
    let hs = this.outputHandlers.get(session);
    if (!hs) { hs = new Set(); this.outputHandlers.set(session, hs); }
    hs.add(fn);
    return () => {
      const cur = this.outputHandlers.get(session);
      if (!cur) return;
      cur.delete(fn);
      if (cur.size === 0) this.outputHandlers.delete(session);
    };
  }
  // onEvent registers fn for all unsolicited messages. Returns an
  // unsubscribe function that removes this handler.
  onEvent(fn) {
    this.eventHandlers.add(fn);
    return () => this.eventHandlers.delete(fn);
  }

  async newSession({ command, args = [], cwd = "", env, cols = 80, rows = 24 }) {
    const r = await this._call({ type: "new_session", command, args, cwd, env, cols, rows });
    return r.session;
  }
  async listSessions() { return (await this._call({ type: "list_sessions" })).sessions || []; }
  async attach(session, { cols = 0, rows = 0 } = {}) { await this._call({ type: "attach", session, cols, rows }); }
  detach(session) { this._send({ type: "detach", session }); }
  writePane(session, text) { this._send({ type: "write_pane", session, data: Buffer.from(text).toString("base64") }); }
  writeBytes(session, buf) { this._send({ type: "write_pane", session, data: Buffer.from(buf).toString("base64") }); }
  // capturePane returns the session's raw scrollback bytes. With
  // { settleMs }, it first waits for the screen to go quiet.
  async capturePane(session, { settleMs = 0, timeoutMs = 0 } = {}) {
    const r = await this._call({ type: "capture_pane", session, settle_ms: settleMs, timeout_ms: timeoutMs });
    return Buffer.from(r.data || "", "base64");
  }
  // captureScreen returns the daemon's authoritative rendered screen (the
  // visible grid, not scrollback): { cols, rows, lines, cursor, altScreen }.
  // With { settleMs }, it first waits for the screen to go quiet - the usual
  // way to read a TUI after sending input.
  async captureScreen(session, { settleMs = 0, timeoutMs = 0 } = {}) {
    const r = await this._call({ type: "capture_pane", session, render: true, settle_ms: settleMs, timeout_ms: timeoutMs });
    return {
      cols: r.cols || 0,
      rows: r.rows || 0,
      lines: r.lines || [],
      cursor: r.cursor || { row: 0, col: 0, visible: true },
      altScreen: !!r.alt_screen,
    };
  }
  resize(session, cols, rows) { this._send({ type: "resize", session, cols, rows }); }
  async kill(session) { await this._call({ type: "kill", session }); }
  // Reap sessions idle (no PTY input/output) for >= maxIdleSeconds;
  // returns the reaped SessionInfo[]. maxIdleSeconds <= 0 reaps all.
  async gc(maxIdleSeconds) { return (await this._call({ type: "gc", max_idle_seconds: maxIdleSeconds })).sessions || []; }
  close() { this.sock.end(); }
}
