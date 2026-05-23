// Unit tests for socket-path discovery — must agree with the Go-side
// implementation in internal/daemon/workspace.go::socketPath.

import { test } from "node:test";
import * as assert from "node:assert/strict";
import * as path from "path";
import * as crypto from "crypto";
import { runtimeDir, socketPath, workspaceID } from "../rpc/socketPath";

test("workspaceID matches sha256(abs)[:16]", () => {
  const repo = "/some/repo/root";
  const want = crypto.createHash("sha256").update(path.resolve(repo)).digest("hex").slice(0, 16);
  assert.equal(workspaceID(repo), want);
});

test("socketPath on linux honors XDG_RUNTIME_DIR", () => {
  const prev = process.env.XDG_RUNTIME_DIR;
  process.env.XDG_RUNTIME_DIR = "/run/user/1000";
  try {
    const got = socketPath("/repo/x", "linux");
    const id = workspaceID("/repo/x");
    assert.equal(got, `/run/user/1000/graph-harness/${id}.sock`);
  } finally {
    if (prev === undefined) delete process.env.XDG_RUNTIME_DIR;
    else process.env.XDG_RUNTIME_DIR = prev;
  }
});

test("socketPath on linux falls back to ~/.cache/graph-harness", () => {
  const prev = process.env.XDG_RUNTIME_DIR;
  delete process.env.XDG_RUNTIME_DIR;
  try {
    const got = socketPath("/repo/x", "linux");
    assert.match(got, /\.cache\/graph-harness\/[0-9a-f]{16}\.sock$/);
  } finally {
    if (prev !== undefined) process.env.XDG_RUNTIME_DIR = prev;
  }
});

test("socketPath on windows uses named-pipe form", () => {
  const got = socketPath("C:\\repo\\x", "win32");
  assert.match(got, /^\\\\\.\\pipe\\graph-harness-[0-9a-f]{16}$/);
});

test("runtimeDir on darwin returns Application Support path", () => {
  const d = runtimeDir("darwin");
  assert.match(d, /Library\/Application Support\/graph-harness$/);
});
