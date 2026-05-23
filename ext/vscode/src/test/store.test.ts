// Unit tests for FrameworkStore — covers cache fill, invalidation, and
// push-event-driven invalidation + change emission.

import { test } from "node:test";
import * as assert from "node:assert/strict";
import { FrameworkStore } from "../framework/store";
import type { EntityLensInfo, KernelEventNotification } from "../framework/types";

test("lookup caches the first query result", async () => {
  let calls = 0;
  const store = new FrameworkStore({
    query: async () => {
      calls++;
      return { bound_flows: 3, active_findings: 1 } as EntityLensInfo;
    },
  });
  const a = await store.lookup("file://a.go", "handler:Foo");
  const b = await store.lookup("file://a.go", "handler:Foo");
  assert.equal(calls, 1, "second lookup should hit cache");
  assert.deepEqual(a, b);
});

test("invalidate forces the next lookup to re-query", async () => {
  let calls = 0;
  const store = new FrameworkStore({
    query: async () => {
      calls++;
      return { bound_flows: calls } as EntityLensInfo;
    },
  });
  const first = await store.lookup("u", "k");
  store.invalidate("u", "k");
  const second = await store.lookup("u", "k");
  assert.equal(first?.bound_flows, 1);
  assert.equal(second?.bound_flows, 2);
  assert.equal(calls, 2);
});

test("applyPushEvent invalidates and fires changed for reanchor events", async () => {
  const fired: string[] = [];
  const store = new FrameworkStore({ query: async () => ({ bound_flows: 0 }) });
  store.onChanged((uri) => fired.push(uri));

  const ev: KernelEventNotification = {
    subscription_id: "sub-1",
    subscriber_id: "vscode:abc",
    seq: 42,
    layer: "code.framework",
    kind: "selector/reanchored",
    payload: { uri: "file://users.go", key: "handler:ListUsers" },
  };
  const affected = store.applyPushEvent(ev);
  assert.deepEqual([...affected], ["file://users.go"]);
  assert.deepEqual(fired, ["file://users.go"]);
});

test("applyPushEvent ignores unrelated event kinds", () => {
  const fired: string[] = [];
  const store = new FrameworkStore({ query: async () => null });
  store.onChanged((u) => fired.push(u));
  const affected = store.applyPushEvent({
    subscription_id: "s",
    subscriber_id: "x",
    seq: 1,
    layer: "code.framework",
    kind: "SchemaAdded", // not in REANCHOR_EVENT_KINDS
    payload: { uri: "file://a", key: "schema_field:x" },
  });
  assert.equal(affected.size, 0);
  assert.deepEqual(fired, []);
});

test("flow/membershipChanged and invariant/violated also trigger re-render", () => {
  const fired = new Set<string>();
  const store = new FrameworkStore({ query: async () => null });
  store.onChanged((u) => fired.add(u));
  store.applyPushEvent({
    subscription_id: "s",
    subscriber_id: "x",
    seq: 1,
    layer: "code.framework",
    kind: "flow/membershipChanged",
    payload: { uri: "file://a", key: "handler:Foo" },
  });
  store.applyPushEvent({
    subscription_id: "s",
    subscriber_id: "x",
    seq: 2,
    layer: "code.framework",
    kind: "invariant/violated",
    payload: { uri: "file://b", key: "schema_field:email" },
  });
  assert.deepEqual([...fired].sort(), ["file://a", "file://b"]);
});

test("payload without uri triggers wildcard invalidation", () => {
  const fired: string[] = [];
  const store = new FrameworkStore({ query: async () => null });
  store.onChanged((u) => fired.push(u));
  store.applyPushEvent({
    subscription_id: "s",
    subscriber_id: "x",
    seq: 9,
    layer: "code.framework",
    kind: "selector/reanchored",
    payload: {},
  });
  assert.deepEqual(fired, ["*"]);
});
