// LSP-style Content-Length framing — the wire codec the daemon uses
// (sourcegraph/jsonrpc2 VSCodeObjectCodec). Each message is:
//
//   Content-Length: <bytes>\r\n
//   \r\n
//   <utf8 json payload of exactly that many bytes>
//
// `FrameReader` accumulates incoming bytes from any stream-like source and
// fires `onMessage` once per complete frame. `encodeFrame` builds a frame
// ready for `socket.write`.

export function encodeFrame(payload: object): Buffer {
  const body = Buffer.from(JSON.stringify(payload), "utf8");
  const header = Buffer.from(`Content-Length: ${body.length}\r\n\r\n`, "ascii");
  return Buffer.concat([header, body]);
}

export type FrameHandler = (msg: unknown) => void;
export type FrameError = (err: Error) => void;

const HEADER_TERMINATOR = Buffer.from("\r\n\r\n", "ascii");

export class FrameReader {
  private buf: Buffer = Buffer.alloc(0);
  // expected body length once we've parsed a complete header block.
  private pendingLen: number | null = null;

  constructor(
    private readonly onMessage: FrameHandler,
    private readonly onError: FrameError = () => undefined,
  ) {}

  /** Feed raw bytes from the socket. May fire `onMessage` zero or more times. */
  push(chunk: Buffer | string): void {
    const data = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk, "utf8");
    this.buf = this.buf.length === 0 ? data : Buffer.concat([this.buf, data]);
    // Drain as many complete frames as we have.
    // (One push may carry partial, single, or many frames.)
    while (true) {
      if (this.pendingLen === null) {
        const sep = this.buf.indexOf(HEADER_TERMINATOR);
        if (sep < 0) return;
        const headers = this.buf.slice(0, sep).toString("ascii");
        this.buf = this.buf.slice(sep + HEADER_TERMINATOR.length);
        const m = /Content-Length:\s*(\d+)/i.exec(headers);
        if (!m) {
          this.onError(new Error(`malformed JSON-RPC frame; headers=${JSON.stringify(headers)}`));
          // Best-effort recovery: drop until we find another terminator.
          this.pendingLen = null;
          continue;
        }
        this.pendingLen = parseInt(m[1], 10);
      }
      if (this.buf.length < this.pendingLen) return;
      const bodyBuf = this.buf.slice(0, this.pendingLen);
      this.buf = this.buf.slice(this.pendingLen);
      this.pendingLen = null;
      try {
        const msg = JSON.parse(bodyBuf.toString("utf8"));
        this.onMessage(msg);
      } catch (e) {
        this.onError(e instanceof Error ? e : new Error(String(e)));
      }
    }
  }

  /** Drop buffered state. Called on disconnect to avoid leaking partial frames. */
  reset(): void {
    this.buf = Buffer.alloc(0);
    this.pendingLen = null;
  }
}
