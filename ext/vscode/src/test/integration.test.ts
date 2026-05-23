// Integration test: spin up an in-memory mock daemon that speaks
// Content-Length-framed JSON-RPC, connect a JsonRpcClient to it, identify
// + subscribe via DaemonBridge, and assert that:
//
//   1. the handshake is correct,
//   2. a selector/reanchored push notification flows into the
//      FrameworkStore and causes the FrameworkLensProvider's
//      onDidChangeCodeLenses emitter to fire,
//   3. reconnect re-issues identify + subscribe with the last-known
//      cursor,
//   4. backoff is exponential and bounded.

import { test } from "node:test";
import * as assert from "node:assert/strict";
import { EventEmitter } from "events";
import { JsonRpcClient, type DuplexLike } from "../rpc/client";
import { FrameReader, encodeFrame } from "../rpc/framing";
import { FrameworkStore } from "../framework/store";
import { FrameworkLensProvider } from "../framework/lensProvider";
import { DaemonBridge } from "../framework/daemonBridge";
import type { KernelEventNotification } from "../framework/types";

/**
 * MockTransport: a pair of EventEmitter-backed duplex shells (client side
 * + server side) connected to one another. write() on one delivers
 * "data" on the other.
 */
class MockTransport extends EventEmitter implements DuplexLike {
  peer: MockTransport | null = null;
  closed = false;
  write(data: Buffer): boolean {
    if (this.closed || !this.peer || this.peer.closed) return false;
    queueMicrotask(() => this.peer?.emit("data", data));
    return true;
  }
  end(): void {
    if (this.closed) return;
    this.closed = true;
    queueMicrotask(() => this.emit("close"));
    this.peer?.end();
  }
  destroy(err?: Error): void {
    if (this.closed) return;
    this.closed = true;
    // Only emit "error" if a listener exists — otherwise EventEmitter
    // promotes it to an unhandled-exception that fails the test runner.
    if (err && this.listenerCount("error") > 0) {
      queueMicrotask(() => this.emit("error", err));
    }
    queueMicrotask(() => this.emit("close"));
    this.peer?.end();
  }
}

function pair(): [MockTransport, MockTransport] {
  const a = new MockTransport();
  const b = new MockTransport();
  a.peer = b;
  b.peer = a;
  return [a, b];
}

interface MockDaemon {
  side: MockTransport;
  reader: FrameReader;
  received: Array<{ id?: unknown; method: string; params: unknown }>;
  notify(method: string, params: unknown): void;
  reply(id: unknown, result: unknown): void;
}

function buildDaemon(side: MockTransport): MockDaemon {
  const daemon: MockDaemon = {
    side,
    reader: new FrameReader(() => undefined),
    received: [],
    notify(method, params) {
      side.write(encodeFrame({ jsonrpc: "2.0", method, params }));
    },
    reply(id, result) {
      side.write(encodeFrame({ jsonrpc: "2.0", id, result }));
    },
  };
  daemon.reader = new FrameReader((msg: unknown) => {
    const m = msg as { id?: unknown; method: string; params: unknown };
    daemon.received.push(m);
    // Auto-reply to known method calls so the handshake completes.
    if (typeof m.id !== "undefined" && m.id !== null) {
      if (m.method === "kernel.identify") {
        daemon.reply(m.id, { subscriber_id: (m.params as { subscriber_id: string }).subscriber_id });
      } else if (m.method === "kernel.subscribe") {
        daemon.reply(m.id, { subscription_id: "sub-test", subscriber_id: "x" });
      } else if (m.method.startsWith("framework.")) {
        daemon.reply(m.id, { info: { bound_flows: 2, active_findings: 0 } });
      }
    }
  });
  side.on("data", (chunk: Buffer) => daemon.reader.push(chunk));
  return daemon;
}

test("DaemonBridge handshake identifies then subscribes on connect", async () => {
  const [clientSide, daemonSide] = pair();
  const daemon = buildDaemon(daemonSide);
  const bridge = new DaemonBridge({ subscriberId: "vscode:test" });
  let connected = false;
  const client = new JsonRpcClient(
    async () => clientSide,
    bridge.clientOptions({ onConnect: () => { connected = true; } }),
  );
  bridge.setClient(client);
  client.start();

  // Wait until the daemon has received the two handshake calls.
  await waitFor(() => daemon.received.length >= 2);
  assert.equal(daemon.received[0].method, "kernel.identify");
  assert.deepEqual(daemon.received[0].params, { subscriber_id: "vscode:test" });
  assert.equal(daemon.received[1].method, "kernel.subscribe");
  const subParams = daemon.received[1].params as Record<string, unknown>;
  assert.equal(subParams.filter, "code.framework");
  assert.equal(subParams.name, "vscode:code.framework");
  assert.equal(connected, true);

  client.stop();
});

test("push kernel.event causes FrameworkLensProvider to fire onDidChangeCodeLenses", async () => {
  const [clientSide, daemonSide] = pair();
  const daemon = buildDaemon(daemonSide);
  const bridge = new DaemonBridge({ subscriberId: "vscode:test" });
  const store = new FrameworkStore({ query: bridge.buildQuery() });
  bridge.attachStore(store);
  const provider = new FrameworkLensProvider(store);

  const client = new JsonRpcClient(async () => clientSide, bridge.clientOptions());
  bridge.setClient(client);
  client.start();

  let fires = 0;
  provider.onDidChangeCodeLenses(() => fires++);

  await waitFor(() => daemon.received.length >= 2);

  // Daemon pushes a selector/reanchored event.
  const ev: KernelEventNotification = {
    subscription_id: "sub-test",
    subscriber_id: "vscode:test",
    seq: 17,
    layer: "code.framework",
    kind: "selector/reanchored",
    payload: { uri: "file:///repo/internal/handlers/users.go", key: "handler:ListUsers" },
  };
  daemon.notify("kernel.event", ev);

  await waitFor(() => fires >= 1);
  assert.equal(fires, 1);
  assert.equal(bridge.cursor(), 17, "cursor must advance to the seq of the last delivered event");

  provider.dispose();
  client.stop();
});

test("reconnect re-issues identify + subscribe with the last-seen cursor", async () => {
  // First connection.
  const [c1, d1] = pair();
  const daemon1 = buildDaemon(d1);
  const bridge = new DaemonBridge({ subscriberId: "vscode:reconnect" });
  const store = new FrameworkStore({ query: bridge.buildQuery() });
  bridge.attachStore(store);

  // The connector returns c1 once, then c2 on reconnect.
  let attempt = 0;
  let c2: MockTransport | null = null;
  let daemon2: MockDaemon | null = null;
  const client = new JsonRpcClient(
    async () => {
      attempt++;
      if (attempt === 1) return c1;
      const [cc2, dd2] = pair();
      c2 = cc2;
      daemon2 = buildDaemon(dd2);
      return cc2;
    },
    bridge.clientOptions({}),
  );
  // Configure tight backoff for the test.
  (client as unknown as { backoffMs: number }).backoffMs = 1;
  (client as unknown as { opts: { initialBackoffMs: number } }).opts.initialBackoffMs = 1;
  bridge.setClient(client);
  client.start();

  await waitFor(() => daemon1.received.length >= 2);

  // Daemon pushes one event so the cursor advances.
  daemon1.notify("kernel.event", {
    subscription_id: "sub-test",
    subscriber_id: "vscode:reconnect",
    seq: 99,
    layer: "code.framework",
    kind: "selector/reanchored",
    payload: { uri: "u", key: "handler:H" },
  });
  await waitFor(() => bridge.cursor() === 99);

  // Kill the first connection — the client should reconnect.
  d1.destroy(new Error("simulated drop"));

  await waitFor(() => attempt === 2 && daemon2 !== null && daemon2.received.length >= 2);
  assert.equal(daemon2!.received[0].method, "kernel.identify");
  assert.equal(daemon2!.received[1].method, "kernel.subscribe");
  const sub = daemon2!.received[1].params as Record<string, unknown>;
  assert.equal(sub.cursor, 99, "reconnect must resume from the last delivered seq");

  client.stop();
});

test("backoff doubles per failure and caps at maxBackoffMs", async () => {
  const delays: number[] = [];
  let attempt = 0;
  const client = new JsonRpcClient(
    async () => {
      attempt++;
      throw new Error("always-fail-attempt-" + attempt);
    },
    {
      initialBackoffMs: 10,
      maxBackoffMs: 80,
      // Capture every scheduled delay and fire immediately so the test
      // doesn't wall-clock through it.
      schedule: (ms, fn) => {
        delays.push(ms);
        const t = setImmediate(fn);
        return () => clearImmediate(t);
      },
    },
  );
  client.start();
  // Wait until we have at least 6 attempts → 5 backoff records.
  await waitFor(() => delays.length >= 5);
  client.stop();
  // First five waits should be 10, 20, 40, 80, 80 (capped).
  assert.deepEqual(delays.slice(0, 5), [10, 20, 40, 80, 80]);
});

// --- helpers ---------------------------------------------------------------

async function waitFor(cond: () => boolean, timeoutMs = 2000): Promise<void> {
  const start = Date.now();
  while (!cond()) {
    if (Date.now() - start > timeoutMs) {
      throw new Error("waitFor timed out");
    }
    await new Promise((r) => setImmediate(r));
  }
}
