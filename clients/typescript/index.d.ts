// Type declarations for the thin Node client (index.mjs). Hand-written
// and kept in sync with index.mjs by hand - the client itself ships as
// plain ESM with zero dependencies and no build step.

/// <reference types="node" />

/** Removes a previously registered handler. Returned by onOutput/onEvent. */
export type Unsubscribe = () => void;

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
}

export interface AttachOptions {
  cols?: number;
  rows?: number;
}

export interface CaptureOptions {
  /** Hold the reply until the PTY is quiet for this many ms (0 = no wait). */
  settleMs?: number;
  /** Cap on the settle wait in ms; <= 0 uses the daemon default. */
  timeoutMs?: number;
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
  /** Connect to the daemon at the given unix socket path. */
  static connect(path: string): Promise<PupptyeerClient>;

  /**
   * Register a handler for a session's live output. Multiple handlers per
   * session are supported (they all fire); returns a function that
   * unsubscribes this handler.
   */
  onOutput(session: string, fn: (bytes: Buffer) => void): Unsubscribe;

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

  /** List metadata for all live sessions. */
  listSessions(): Promise<SessionInfo[]>;

  /** Subscribe this connection to the session's live output. */
  attach(session: string, opts?: AttachOptions): Promise<void>;

  /** Stop this connection's subscription to the session. */
  detach(session: string): void;

  /** Send UTF-8 text to the session's PTY input. */
  writePane(session: string, text: string): void;

  /** Send raw bytes to the session's PTY input. */
  writeBytes(session: string, buf: Uint8Array | Buffer): void;

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
  resize(session: string, cols: number, rows: number): void;

  /** Terminate the session's PTY. */
  kill(session: string): Promise<void>;

  /**
   * Reap sessions idle (no PTY input/output) for >= maxIdleSeconds;
   * resolves to the reaped SessionInfo[]. maxIdleSeconds <= 0 reaps all.
   */
  gc(maxIdleSeconds: number): Promise<SessionInfo[]>;

  /** Close the connection. Sessions outlive the client. */
  close(): void;
}
