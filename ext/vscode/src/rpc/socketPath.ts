// Workspace → daemon-socket path resolver. Mirrors the Go logic in
// internal/daemon/workspace.go::socketPath so the extension can dial the
// same Unix-domain socket (or named pipe on Windows) the CLI talks to —
// SPEC §9.4: there is no second protocol.
//
// The workspace id is sha256(absolute repo root) truncated to 16 hex chars,
// matching `internal/daemon/workspace.go::workspaceID`.

import * as os from "os";
import * as path from "path";
import * as crypto from "crypto";
import * as fs from "fs";

/** Compute the deterministic per-workspace id (16 hex chars). */
export function workspaceID(repoRoot: string): string {
  const abs = path.resolve(repoRoot);
  return crypto.createHash("sha256").update(abs).digest("hex").slice(0, 16);
}

/**
 * Walk upward from `start` until a `.graph-harness` directory is found.
 * Returns the workspace root (the directory *containing* `.graph-harness`)
 * or `null` if none is found before the filesystem root.
 */
export function discoverWorkspaceRoot(start: string): string | null {
  let dir = path.resolve(start);
  // Cap iterations as a paranoia guard against unbounded loops.
  for (let i = 0; i < 256; i++) {
    const candidate = path.join(dir, ".graph-harness");
    try {
      const stat = fs.statSync(candidate);
      if (stat.isDirectory()) {
        return dir;
      }
    } catch {
      /* not present; keep walking */
    }
    const parent = path.dirname(dir);
    if (parent === dir) return null;
    dir = parent;
  }
  return null;
}

/**
 * Compute the runtime directory holding workspace sockets. Mirrors
 * `internal/daemon/workspace.go::runtimeDir`.
 *
 * - linux: $XDG_RUNTIME_DIR/graph-harness or ~/.cache/graph-harness
 * - darwin: ~/Library/Application Support/graph-harness
 * - win32: %LOCALAPPDATA%/graph-harness
 */
export function runtimeDir(platform: NodeJS.Platform = process.platform): string {
  if (platform === "linux") {
    const xdg = process.env.XDG_RUNTIME_DIR;
    if (xdg) return path.join(xdg, "graph-harness");
    return path.join(os.homedir(), ".cache", "graph-harness");
  }
  if (platform === "darwin") {
    return path.join(os.homedir(), "Library", "Application Support", "graph-harness");
  }
  if (platform === "win32") {
    const base = process.env.LOCALAPPDATA || path.join(os.homedir(), "AppData", "Local");
    return path.join(base, "graph-harness");
  }
  // Unsupported platforms fall back to a tmp path so callers fail at dial
  // rather than at path construction.
  return path.join(os.tmpdir(), "graph-harness");
}

/**
 * Compute the socket / named-pipe path for `repoRoot`. Linux/macOS return
 * a unix-socket filesystem path; Windows returns a `\\.\pipe\…` path.
 */
export function socketPath(repoRoot: string, platform: NodeJS.Platform = process.platform): string {
  const id = workspaceID(repoRoot);
  if (platform === "win32") {
    return `\\\\.\\pipe\\graph-harness-${id}`;
  }
  return path.join(runtimeDir(platform), `${id}.sock`);
}
