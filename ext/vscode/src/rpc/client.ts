// Minimal JSON-RPC 2.0 client over a duplex (net.Socket-shaped) transport.
//
// Why custom: we want a thin, dependency-free client that handles
//   - Content-Length framing (the daemon's wire codec),
//   - request/response correlation by id,
//   - server-pushed notifications (kernel.event, kernel.fellBehind),
//   - exponential-backoff reconnect with the last-known cursor re-attached.
//
// The transport is abstracted behind `Connector` so tests can substitute an
// in-memory duplex without touching the OS socket layer.

import { EventEmitter } from "events";
import * as net from "net";
import { FrameReader, encodeFrame } from "./framing";

export interface DuplexLike {
  write(data: Buffer): boolean;
  on(event: "data", listener: (chunk: Buffer) => void): this;
  on(event: "close", listener: () => void): this;
  on(event: "error", listener: (err: Error) => void): this;
  on(event: "end", listener: () => void): this;
  end(): void;
  destroy(err?: Error): void;
}

export type Connector = () => Promise<DuplexLike>;

/** Build a Connector that dials a Unix-domain socket (or named pipe on Windows). */
export function unixSocketConnector(socketPath: string): Connector {
  return async () =>
    new Promise<DuplexLike>((resolve, reject) => {
      const sock = net.createConnection(socketPath);
      const onError = (err: Error) => {
        sock.removeAllListeners("connect");
        reject(err);
      };
      sock.once("error", onError);
      sock.once("connect", () => {
        sock.off("error", onError);
        resolve(sock);
      });
    });
}

interface PendingCall {
  resolve: (value: unknown) => void;
  reject: (err: Error) => void;
}

export type NotificationHandler = (method: string, params: unknown) => void;

export interface ClientOptions {
  /** Called whenever a server-pushed notification arrives. */
  onNotification?: NotificationHandler;
  /** Called every time a connection is established (post-dial, pre-handshake). */
  onConnect?: () => void | Promise<void>;
  /** Called whenever the underlying transport drops. */
  onDisconnect?: (err?: Error) => void;
  /** Initial reconnect delay, doubled per failure. Default 500ms. */
  initialBackoffMs?: number;
  /** Max reconnect delay. Default 30_000ms. */
  maxBackoffMs?: number;
  /** Inject a clock for tests. */
  now?: () => number;
  /** Inject a timer for tests (returns a cancel func). */
  schedule?: (ms: number, fn: () => void) => () => void;
}

const DEFAULT_INITIAL_BACKOFF = 500;
const DEFAULT_MAX_BACKOFF = 30_000;

/**
 * JSON-RPC client with auto-reconnect.
 *
 * The client owns a single live connection at a time. `start()` kicks off
 * the connect loop; `stop()` shuts it down permanently. Pending `call`s
 * that race a disconnect reject with `ConnectionLost` — caller decides
 * whether to retry once `onConnect` fires again.
 */
export class JsonRpcClient extends EventEmitter {
  private nextId = 1;
  private pending = new Map<number, PendingCall>();
  private transport: DuplexLike | null = null;
  private reader = new FrameReader(
    (msg) => this.dispatch(msg),
    (err) => this.emit("framingError", err),
  );
  private stopped = false;
  private backoffMs: number;
  private readonly opts: Required<Pick<ClientOptions,
    "initialBackoffMs" | "maxBackoffMs"
  >> & ClientOptions;
  private cancelTimer: (() => void) | null = null;

  constructor(
    private readonly connector: Connector,
    opts: ClientOptions = {},
  ) {
    super();
    this.opts = {
      initialBackoffMs: opts.initialBackoffMs ?? DEFAULT_INITIAL_BACKOFF,
      maxBackoffMs: opts.maxBackoffMs ?? DEFAULT_MAX_BACKOFF,
      onNotification: opts.onNotification,
      onConnect: opts.onConnect,
      onDisconnect: opts.onDisconnect,
      now: opts.now,
      schedule: opts.schedule,
    };
    this.backoffMs = this.opts.initialBackoffMs;
  }

  /** Begin connecting; reconnects with exponential backoff on failure. */
  start(): void {
    this.stopped = false;
    void this.connectLoop();
  }

  /** Stop reconnecting and close the live connection (if any). */
  stop(): void {
    this.stopped = true;
    if (this.cancelTimer) {
      this.cancelTimer();
      this.cancelTimer = null;
    }
    const t = this.transport;
    this.transport = null;
    if (t) {
      try {
        t.end();
      } catch {
        /* swallow */
      }
    }
    for (const [, p] of this.pending) {
      p.reject(new ConnectionLost("client stopped"));
    }
    this.pending.clear();
  }

  isConnected(): boolean {
    return this.transport !== null;
  }

  /** Issue a JSON-RPC call. Rejects if the connection drops mid-flight. */
  call<T = unknown>(method: string, params?: unknown): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      const t = this.transport;
      if (!t) {
        reject(new ConnectionLost("not connected"));
        return;
      }
      const id = this.nextId++;
      this.pending.set(id, {
        resolve: (v) => resolve(v as T),
        reject,
      });
      const frame = encodeFrame({
        jsonrpc: "2.0",
        id,
        method,
        params: params ?? null,
      });
      try {
        t.write(frame);
      } catch (e) {
        this.pending.delete(id);
        reject(e instanceof Error ? e : new Error(String(e)));
      }
    });
  }

  /** Fire-and-forget JSON-RPC notification (no response expected). */
  notify(method: string, params?: unknown): void {
    const t = this.transport;
    if (!t) return;
    const frame = encodeFrame({ jsonrpc: "2.0", method, params: params ?? null });
    try {
      t.write(frame);
    } catch {
      /* notifications are best-effort */
    }
  }

  private async connectLoop(): Promise<void> {
    while (!this.stopped) {
      try {
        const t = await this.connector();
        this.attach(t);
        this.backoffMs = this.opts.initialBackoffMs;
        if (this.opts.onConnect) {
          try {
            await this.opts.onConnect();
          } catch (e) {
            // Handshake failed; tear down + retry.
            this.detach(e instanceof Error ? e : new Error(String(e)));
            await this.waitBackoff();
            continue;
          }
        }
        // Wait for disconnect; the `close` listener resolves this promise
        // via `_disconnected` so the loop can re-enter.
        await this.waitForDisconnect();
      } catch (err) {
        this.emit("connectError", err);
        if (this.stopped) return;
        await this.waitBackoff();
      }
    }
  }

  private _disconnected: (() => void) | null = null;
  private waitForDisconnect(): Promise<void> {
    return new Promise((resolve) => {
      this._disconnected = resolve;
    });
  }

  private attach(t: DuplexLike): void {
    this.transport = t;
    this.reader.reset();
    t.on("data", (chunk) => this.reader.push(chunk));
    const onClose = () => this.detach();
    t.on("close", onClose);
    t.on("end", onClose);
    t.on("error", (err) => this.detach(err));
  }

  private detach(err?: Error): void {
    if (!this.transport) return;
    this.transport = null;
    for (const [, p] of this.pending) {
      p.reject(new ConnectionLost(err?.message ?? "transport closed"));
    }
    this.pending.clear();
    this.opts.onDisconnect?.(err);
    const cb = this._disconnected;
    this._disconnected = null;
    cb?.();
  }

  // waitBackoff sleeps once before the next connect attempt. It is the
  // ONLY setTimeout in the production extension path — explicitly *not* a
  // polling timer (no recurring tick, no liveness re-check). Lens
  // freshness is driven entirely by server-pushed kernel.event
  // notifications. See README "Push subscription contract".
  private waitBackoff(): Promise<void> {
    return new Promise((resolve) => {
      const ms = this.backoffMs;
      this.backoffMs = Math.min(this.backoffMs * 2, this.opts.maxBackoffMs);
      if (this.opts.schedule) {
        this.cancelTimer = this.opts.schedule(ms, () => {
          this.cancelTimer = null;
          resolve();
        });
      } else {
        const handle = setTimeout(() => {
          this.cancelTimer = null;
          resolve();
        }, ms);
        // Allow process exit if extension deactivates mid-backoff.
        if (typeof (handle as { unref?: () => void }).unref === "function") {
          (handle as { unref: () => void }).unref();
        }
        this.cancelTimer = () => clearTimeout(handle);
      }
    });
  }

  private dispatch(msg: unknown): void {
    if (!msg || typeof msg !== "object") return;
    const m = msg as { id?: unknown; method?: unknown; result?: unknown; error?: unknown; params?: unknown };
    if (typeof m.method === "string" && (m.id === undefined || m.id === null)) {
      // Notification.
      this.opts.onNotification?.(m.method, m.params);
      this.emit("notification", m.method, m.params);
      return;
    }
    if (typeof m.id === "number") {
      const p = this.pending.get(m.id);
      if (!p) return;
      this.pending.delete(m.id);
      if (m.error) {
        const e = m.error as { code?: number; message?: string };
        p.reject(new JsonRpcError(e.code ?? -32000, e.message ?? "unknown error"));
      } else {
        p.resolve(m.result);
      }
    }
  }
}

export class ConnectionLost extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ConnectionLost";
  }
}

export class JsonRpcError extends Error {
  constructor(public code: number, message: string) {
    super(message);
    this.name = "JsonRpcError";
  }
}
