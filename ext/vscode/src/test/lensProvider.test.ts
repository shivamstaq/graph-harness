// Unit tests for FrameworkLensProvider — covers the pure title-building
// logic (singular/plural correctness, subscriber row for publishers,
// no-binding fallback) and the onChanged → emitter wiring.

import { test } from "node:test";
import * as assert from "node:assert/strict";
import { buildCommand, FrameworkLensProvider } from "../framework/lensProvider";
import { FrameworkStore } from "../framework/store";

test("buildCommand pluralizes counts and includes findings", () => {
  const cmd = buildCommand(
    { uri: "u", key: "handler:Foo", kind: "handler" },
    { bound_flows: 2, active_findings: 1 },
  );
  assert.equal(cmd.title, "2 bound flows  ·  1 active finding");
  assert.equal(cmd.command, "graphHarness.framework.showDetails");
});

test("buildCommand singularizes when count is 1", () => {
  const cmd = buildCommand(
    { uri: "u", key: "handler:F", kind: "handler" },
    { bound_flows: 1, active_findings: 1 },
  );
  assert.equal(cmd.title, "1 bound flow  ·  1 active finding");
});

test("buildCommand for event_publisher includes subscriber count", () => {
  const cmd = buildCommand(
    { uri: "u", key: "event_publisher:kafka.NewWriter", kind: "event_publisher" },
    { bound_flows: 0, active_findings: 0, subscribers: 4 },
  );
  assert.equal(cmd.title, "0 bound flows  ·  0 active findings  ·  4 subscribers");
});

test("buildCommand falls back to 'no framework binding' when info is null", () => {
  const cmd = buildCommand(
    { uri: "u", key: "handler:Foo", kind: "handler" },
    null,
  );
  assert.match(cmd.title, /no framework binding/);
});

test("FrameworkLensProvider fires onDidChangeCodeLenses when the store reports change", async () => {
  const store = new FrameworkStore({ query: async () => null });
  const provider = new FrameworkLensProvider(store);
  let fires = 0;
  const sub = provider.onDidChangeCodeLenses(() => fires++);
  // Simulate a push event that the store maps to a single-uri invalidation.
  store.applyPushEvent({
    subscription_id: "s",
    subscriber_id: "x",
    seq: 7,
    layer: "code.framework",
    kind: "selector/reanchored",
    payload: { uri: "file://users.go", key: "handler:ListUsers" },
  });
  assert.equal(fires, 1);
  sub.dispose();
  provider.dispose();
});
