// Type declarations for the thin Node client (index.mjs). Hand-written
// and kept in sync with index.mjs by hand - the client itself ships as
// plain ESM with zero dependencies and no build step.

/// <reference types="node" />

/** Removes a previously registered handler. Returned by onOutput/onEvent. */
export type Unsubscribe = () => void;

/**
 * The namespace a connection uses when none is given. Session identity is
 * (namespace, id): ids are unique within a namespace but may repeat across
 * namespaces.
 */
export const DEFAULT_NAMESPACE: "default";

/**
 * Resolve the daemon socket path the way the CLI and the other clients do:
 * $PUPPTYEER_SOCK, else $XDG_RUNTIME_DIR/pupptyeer/daemon.sock, else
 * $TMPDIR/pupptyeer-<uid>/daemon.sock.
 */
export function defaultSocketPath(): string;

/** A single unsolicited message from the daemon (output, exit, etc.). */
export interface Message {
  type: string;
  id?: number;
  session?: string;
  data?: string;
  exit_code?: number;
  message?: string;
  sessions?: SessionInfo[];
  [key: string]: unknown;
}

/** Metadata for a live session, as returned by listSessions/gc. */
export interface SessionInfo {
  id: string;
  /** The namespace this session lives in. */
  namespace: string;
  command: string;
  args?: string[];
  cwd?: string;
  cols: number;
  rows: number;
  created: string;
  last_activity: string;
  attached: number;
  alive: boolean;
}

export interface NewSessionOptions {
  command: string;
  args?: string[];
  cwd?: string;
  env?: Record<string, string>;
  cols?: number;
  rows?: number;
  /** Run no terminal emulator (lower CPU/latency); rendered capture is then unavailable. */
  raw?: boolean;
  /** Use this string as the session id instead of a daemon-generated UUID. */
  requestedId?: string;
  /** When an alive session already holds requestedId, return it as-is (continuation) instead of erroring. */
  getOrCreate?: boolean;
  /** Namespace to create the session in (default: the connection's). */
  namespace?: string;
}

export interface EnsureSessionOptions {
  /** The session id to continue or create. */
  id: string;
  command: string;
  args?: string[];
  cwd?: string;
  env?: Record<string, string>;
  cols?: number;
  rows?: number;
  raw?: boolean;
  /** Namespace to operate in (default: the connection's). */
  namespace?: string;
}

/** Options for connect: an explicit socket and/or a default namespace. */
export interface ConnectOptions {
  /** Explicit daemon socket path; omitted resolves the default location. */
  socket?: string;
  /** Connection default namespace; per-call options override it. */
  namespace?: string;
}

/** A per-call namespace override, shared by the session-addressed methods. */
export interface NamespaceOption {
  namespace?: string;
}

/** Options for listSessions/gc: a namespace filter or the all-namespaces view. */
export interface ListOptions {
  /** Namespace to scope to (default: the connection's); ignored when all is true. */
  namespace?: string;
  /** Select every namespace instead of just one. */
  all?: boolean;
}

export interface AttachOptions {
  cols?: number;
  rows?: number;
  /** Namespace override (default: the connection's). */
  namespace?: string;
}

export interface CaptureOptions {
  /** Hold the reply until the PTY is quiet for this many ms (0 = no wait). */
  settleMs?: number;
  /** Cap on the settle wait in ms; <= 0 uses the daemon default. */
  timeoutMs?: number;
  /** Namespace override (default: the connection's). */
  namespace?: string;
}

/** Cursor position in a rendered capture; 0-based, col may equal cols. */
export interface Cursor {
  row: number;
  col: number;
  visible: boolean;
}

/** The rendered visible terminal grid returned by captureScreen. */
export interface Screen {
  cols: number;
  rows: number;
  /** Exactly `rows` strings, each space-padded to `cols`. */
  lines: string[];
  cursor: Cursor;
  altScreen: boolean;
}

/** Thin client for the pupptyeer daemon (NDJSON over a unix socket). */
export class PupptyeerClient {
  /** The connection's default namespace. */
  readonly namespace: string;

  /**
   * Connect to the daemon. Pass a socket path string, or an options object
   * { socket?, namespace? }. With no socket it resolves the default location;
   * on an unreachable daemon it rejects with one canonical, actionable error.
   * It never spawns a daemon.
   */
  static connect(opts?: string | ConnectOptions): Promise<PupptyeerClient>;

  /**
   * Register a handler for a session's live output. Multiple handlers per
   * session are supported (they all fire); returns a function that
   * unsubscribes this handler. Output is keyed by (namespace, session).
   */
  onOutput(session: string, fn: (bytes: Buffer) => void, opts?: NamespaceOption): Unsubscribe;

  /**
   * Register a handler for every unsolicited message. Returns a function
   * that unsubscribes this handler.
   */
  onEvent(fn: (msg: Message) => void): Unsubscribe;

  /** Spawn command in a fresh PTY; resolves to the new session id. */
  newSession(opts: NewSessionOptions): Promise<string>;

  /**
   * Continue if alive, else create: if an alive session already holds
   * opts.id it is returned (resolves false); otherwise a new session is
   * spawned with that id (resolves true). command/args/cwd/env/cols/rows are
   * used only when a session is actually created.
   */
  ensureSession(opts: EnsureSessionOptions): Promise<boolean>;

  /**
   * List metadata for live sessions in opts.namespace (default: the
   * connection's), or across every namespace with { all: true }.
   */
  listSessions(opts?: ListOptions): Promise<SessionInfo[]>;

  /** Subscribe this connection to the session's live output. */
  attach(session: string, opts?: AttachOptions): Promise<void>;

  /** Stop this connection's subscription to the session. */
  detach(session: string, opts?: NamespaceOption): void;

  /** Send UTF-8 text to the session's PTY input. */
  writePane(session: string, text: string, opts?: NamespaceOption): void;

  /** Send raw bytes to the session's PTY input. */
  writeBytes(session: string, buf: Uint8Array | Buffer, opts?: NamespaceOption): void;

  /**
   * Snapshot the session's raw scrollback bytes. With opts.settleMs, first
   * waits for the screen to go quiet.
   */
  capturePane(session: string, opts?: CaptureOptions): Promise<Buffer>;

  /**
   * Return the daemon's authoritative rendered screen (the visible grid,
   * not scrollback). With opts.settleMs, first waits for the screen to go
   * quiet - the usual way to read a TUI after sending input.
   */
  captureScreen(session: string, opts?: CaptureOptions): Promise<Screen>;

  /** Update this client's desired size for the session. */
  resize(session: string, cols: number, rows: number, opts?: NamespaceOption): void;

  /** Terminate the session's PTY. */
  kill(session: string, opts?: NamespaceOption): Promise<void>;

  /**
   * Reap sessions idle (no PTY input/output) for >= maxIdleSeconds in
   * opts.namespace (default: the connection's) or across every namespace with
   * { all: true }; resolves to the reaped SessionInfo[]. maxIdleSeconds <= 0
   * reaps all (matching).
   */
  gc(maxIdleSeconds: number, opts?: ListOptions): Promise<SessionInfo[]>;

  /** Close the connection. Sessions outlive the client. */
  close(): void;
}
