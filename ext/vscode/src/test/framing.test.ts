// Unit tests for the LSP-style Content-Length frame reader.

import { test } from "node:test";
import * as assert from "node:assert/strict";
import { FrameReader, encodeFrame } from "../rpc/framing";

test("encodeFrame round-trips a single JSON message", () => {
  const got: unknown[] = [];
  const r = new FrameReader((m) => got.push(m));
  const msg = { jsonrpc: "2.0", id: 1, method: "ping" };
  r.push(encodeFrame(msg));
  assert.deepEqual(got, [msg]);
});

test("FrameReader handles split frames across pushes", () => {
  const got: unknown[] = [];
  const r = new FrameReader((m) => got.push(m));
  const buf = encodeFrame({ ok: true });
  // Push the frame one byte at a time.
  for (let i = 0; i < buf.length; i++) r.push(buf.slice(i, i + 1));
  assert.deepEqual(got, [{ ok: true }]);
});

test("FrameReader drains multiple frames from one push", () => {
  const got: unknown[] = [];
  const r = new FrameReader((m) => got.push(m));
  const combined = Buffer.concat([
    encodeFrame({ seq: 1 }),
    encodeFrame({ seq: 2 }),
    encodeFrame({ seq: 3 }),
  ]);
  r.push(combined);
  assert.deepEqual(got, [{ seq: 1 }, { seq: 2 }, { seq: 3 }]);
});

test("FrameReader reset clears partial buffers", () => {
  const got: unknown[] = [];
  const r = new FrameReader((m) => got.push(m));
  const buf = encodeFrame({ a: 1 });
  r.push(buf.slice(0, 10)); // partial header
  r.reset();
  r.push(encodeFrame({ b: 2 }));
  assert.deepEqual(got, [{ b: 2 }]);
});

test("FrameReader rejects malformed headers via onError", () => {
  const errs: Error[] = [];
  const got: unknown[] = [];
  const r = new FrameReader(
    (m) => got.push(m),
    (e) => errs.push(e),
  );
  r.push(Buffer.from("Garbage: yes\r\n\r\n", "ascii"));
  assert.equal(errs.length, 1);
  assert.match(errs[0].message, /malformed/i);
});
