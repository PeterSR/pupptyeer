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
    this.outputHandlers = new Map(); // session -> fn(Buffer)
    this.eventHandlers = []; // fn(msg)
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
    if (msg.type === "output" && this.outputHandlers.has(msg.session)) {
      this.outputHandlers.get(msg.session)(Buffer.from(msg.data || "", "base64"));
    }
    for (const fn of this.eventHandlers) fn(msg);
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

  onOutput(session, fn) { this.outputHandlers.set(session, fn); }
  onEvent(fn) { this.eventHandlers.push(fn); }

  async newSession({ command, args = [], cwd = "", env, cols = 80, rows = 24 }) {
    const r = await this._call({ type: "new_session", command, args, cwd, env, cols, rows });
    return r.session;
  }
  async listSessions() { return (await this._call({ type: "list_sessions" })).sessions || []; }
  async attach(session, { cols = 0, rows = 0 } = {}) { await this._call({ type: "attach", session, cols, rows }); }
  detach(session) { this._send({ type: "detach", session }); }
  writePane(session, text) { this._send({ type: "write_pane", session, data: Buffer.from(text).toString("base64") }); }
  writeBytes(session, buf) { this._send({ type: "write_pane", session, data: Buffer.from(buf).toString("base64") }); }
  async capturePane(session) {
    const r = await this._call({ type: "capture_pane", session });
    return Buffer.from(r.data || "", "base64");
  }
  resize(session, cols, rows) { this._send({ type: "resize", session, cols, rows }); }
  async kill(session) { await this._call({ type: "kill", session }); }
  // Reap sessions idle (no PTY input/output) for >= maxIdleSeconds;
  // returns the reaped SessionInfo[]. maxIdleSeconds <= 0 reaps all.
  async gc(maxIdleSeconds) { return (await this._call({ type: "gc", max_idle_seconds: maxIdleSeconds })).sessions || []; }
  close() { this.sock.end(); }
}
