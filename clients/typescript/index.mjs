// Thin Node client for the pupptyeer daemon (NDJSON over a unix
// socket). Zero dependencies - uses the built-in `net` module.
//
//   import { PupptyeerClient } from "./index.mjs";
//   const c = await PupptyeerClient.connect({ namespace: "my-app" });
//   const id = await c.newSession({ command: "bash" });
//   c.onOutput(id, (bytes) => process.stdout.write(bytes));
//   await c.attach(id, { cols: 80, rows: 24 });
//   await c.writePane(id, "echo hi\n");

import net from "node:net";
import os from "node:os";
import path from "node:path";

// DEFAULT_NAMESPACE is the namespace a connection uses when none is given.
// Session identity is (namespace, id): ids are unique within a namespace but
// may repeat across namespaces. A client that never sets a namespace operates
// entirely here, matching pre-namespace behaviour.
export const DEFAULT_NAMESPACE = "default";

// defaultSocketPath resolves the daemon socket the same way the pupptyeer CLI
// and the other clients do: $PUPPTYEER_SOCK, else
// $XDG_RUNTIME_DIR/pupptyeer/daemon.sock, else $TMPDIR/pupptyeer-<uid>/daemon.sock
// (<uid> is the numeric uid on POSIX, "shared" where unavailable).
export function defaultSocketPath() {
  if (process.env.PUPPTYEER_SOCK) return process.env.PUPPTYEER_SOCK;
  if (process.env.XDG_RUNTIME_DIR) return path.join(process.env.XDG_RUNTIME_DIR, "pupptyeer", "daemon.sock");
  const uid = typeof process.getuid === "function" ? String(process.getuid()) : "shared";
  return path.join(os.tmpdir(), "pupptyeer-" + uid, "daemon.sock");
}

export class PupptyeerClient {
  constructor(socket, namespace = DEFAULT_NAMESPACE) {
    this.sock = socket;
    this.namespace = namespace || DEFAULT_NAMESPACE;
    this.nextId = 0;
    this.pending = new Map(); // id -> {resolve, reject}
    this.outputHandlers = new Map(); // "ns\tsession" -> Set<fn(Buffer)>
    this.eventHandlers = new Set(); // Set<fn(msg)>
    this._buf = "";
    socket.on("data", (chunk) => this._onData(chunk));
    socket.on("close", () => {
      for (const { reject } of this.pending.values()) reject(new Error("connection closed"));
      this.pending.clear();
    });
  }

  // connect is the single entry point: pass a socket path string, or an
  // options object { socket?, namespace? }. With no socket it resolves the
  // default location (defaultSocketPath); on an unreachable daemon it rejects
  // with ONE canonical, actionable error. It never spawns a daemon.
  static connect(opts) {
    let socket, namespace;
    if (typeof opts === "string") {
      socket = opts;
    } else if (opts && typeof opts === "object") {
      socket = opts.socket;
      namespace = opts.namespace;
    }
    const sock = socket || defaultSocketPath();
    return new Promise((resolve, reject) => {
      const s = net.createConnection({ path: sock }, () => resolve(new PupptyeerClient(s, namespace)));
      s.once("error", (err) =>
        reject(new Error(
          `no pupptyeer daemon at ${sock}; install/start it (\`pupptyeer daemon install\`) or set PUPPTYEER_SOCK: ${err.message}`,
        )));
    });
  }

  // _ns resolves a per-call namespace override against the connection default.
  _ns(ns) { return ns || this.namespace; }
  _key(ns, session) { return this._ns(ns) + "\t" + session; }

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
      const hs = this.outputHandlers.get(this._key(msg.namespace, msg.session));
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
  // function that removes this handler. An optional { namespace } overrides
  // the connection default (output is keyed by (namespace, session)).
  onOutput(session, fn, { namespace } = {}) {
    const key = this._key(namespace, session);
    let hs = this.outputHandlers.get(key);
    if (!hs) { hs = new Set(); this.outputHandlers.set(key, hs); }
    hs.add(fn);
    return () => {
      const cur = this.outputHandlers.get(key);
      if (!cur) return;
      cur.delete(fn);
      if (cur.size === 0) this.outputHandlers.delete(key);
    };
  }
  // onEvent registers fn for all unsolicited messages. Returns an
  // unsubscribe function that removes this handler.
  onEvent(fn) {
    this.eventHandlers.add(fn);
    return () => this.eventHandlers.delete(fn);
  }

  // raw: true creates a session with no terminal emulator on the daemon (lower
  // CPU/latency); rendered capture (captureScreen) is then unavailable, raw
  // capturePane still works.
  // requestedId: use this string as the session id instead of a daemon UUID.
  // getOrCreate: when an alive session already holds requestedId, return it as
  // is (continuation) instead of erroring on the clash.
  // namespace selects the session's namespace (default: the connection's).
  async newSession({ command, args = [], cwd = "", env, cols = 80, rows = 24, raw = false, requestedId = "", getOrCreate = false, namespace }) {
    const r = await this._call({ type: "new_session", namespace: this._ns(namespace), command, args, cwd, env, cols, rows, raw, requested_id: requestedId, get_or_create: getOrCreate });
    return r.session;
  }
  // ensureSession is "continue if alive, else create": if an alive session
  // already holds id it is returned (returns false); otherwise a new session is
  // spawned with that id (returns true). command/args/cwd/env/cols/rows are used
  // only when a session is actually created.
  async ensureSession({ id, command, args = [], cwd = "", env, cols = 80, rows = 24, raw = false, namespace }) {
    const ns = this._ns(namespace);
    const sessions = await this.listSessions({ namespace: ns });
    if (sessions.find((s) => s.id === id && s.alive)) return false;
    await this.newSession({ command, args, cwd, env, cols, rows, raw, requestedId: id, getOrCreate: true, namespace: ns });
    return true;
  }
  // listSessions lists sessions in { namespace } (default: the connection's),
  // or across every namespace with { all: true }.
  async listSessions({ namespace, all = false } = {}) {
    const msg = all ? { type: "list_sessions", all: true } : { type: "list_sessions", namespace: this._ns(namespace) };
    return (await this._call(msg)).sessions || [];
  }
  async attach(session, { cols = 0, rows = 0, namespace } = {}) { await this._call({ type: "attach", namespace: this._ns(namespace), session, cols, rows }); }
  detach(session, { namespace } = {}) { this._send({ type: "detach", namespace: this._ns(namespace), session }); }
  writePane(session, text, { namespace } = {}) { this._send({ type: "write_pane", namespace: this._ns(namespace), session, data: Buffer.from(text).toString("base64") }); }
  writeBytes(session, buf, { namespace } = {}) { this._send({ type: "write_pane", namespace: this._ns(namespace), session, data: Buffer.from(buf).toString("base64") }); }
  // capturePane returns the session's raw scrollback bytes. With
  // { settleMs }, it first waits for the screen to go quiet.
  async capturePane(session, { settleMs = 0, timeoutMs = 0, namespace } = {}) {
    const r = await this._call({ type: "capture_pane", namespace: this._ns(namespace), session, settle_ms: settleMs, timeout_ms: timeoutMs });
    return Buffer.from(r.data || "", "base64");
  }
  // captureScreen returns the daemon's authoritative rendered screen (the
  // visible grid, not scrollback): { cols, rows, lines, cursor, altScreen }.
  // With { settleMs }, it first waits for the screen to go quiet - the usual
  // way to read a TUI after sending input.
  async captureScreen(session, { settleMs = 0, timeoutMs = 0, namespace } = {}) {
    const r = await this._call({ type: "capture_pane", namespace: this._ns(namespace), session, render: true, settle_ms: settleMs, timeout_ms: timeoutMs });
    return {
      cols: r.cols || 0,
      rows: r.rows || 0,
      lines: r.lines || [],
      // An omitted cursor means the daemon didn't report one; default it to
      // not-visible so callers treat an unknown cursor as untrustworthy rather
      // than as a real cursor parked at row 0 (matches the Go client, whose
      // nil/zero-value cursor is not visible).
      cursor: r.cursor || { row: 0, col: 0, visible: false },
      altScreen: !!r.alt_screen,
    };
  }
  resize(session, cols, rows, { namespace } = {}) { this._send({ type: "resize", namespace: this._ns(namespace), session, cols, rows }); }
  async kill(session, { namespace } = {}) { await this._call({ type: "kill", namespace: this._ns(namespace), session }); }
  // Reap sessions idle (no PTY input/output) for >= maxIdleSeconds in
  // { namespace } (default: the connection's) or across every namespace with
  // { all: true }; returns the reaped SessionInfo[]. maxIdleSeconds <= 0 reaps all.
  async gc(maxIdleSeconds, { namespace, all = false } = {}) {
    const msg = all
      ? { type: "gc", max_idle_seconds: maxIdleSeconds, all: true }
      : { type: "gc", max_idle_seconds: maxIdleSeconds, namespace: this._ns(namespace) };
    return (await this._call(msg)).sessions || [];
  }
  close() { this.sock.end(); }
}
