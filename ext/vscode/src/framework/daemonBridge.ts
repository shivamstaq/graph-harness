// DaemonBridge wires the JSON-RPC client to the FrameworkStore:
//
//   - On every connect, calls `kernel.identify({subscriber_id})` then
//     `kernel.subscribe({filter: "code.framework", cursor})`.
//   - Routes `kernel.event` notifications into store.applyPushEvent.
//   - Tracks the last-seen seq so reconnect resumes from cursor+1.
//   - Exposes a `query` function the store uses for cache-fill RPCs
//     (`framework.routes` / `framework.event_publishers` /
//     `framework.schema_fields`).
//
// The bridge is push-only: there is no setInterval / setTimeout polling
// loop. Reconnect is driven by the JsonRpcClient's exponential backoff.

import { JsonRpcClient, type ClientOptions } from "../rpc/client";
import type { FrameworkStore } from "./store";
import type { EntityLensInfo, KernelEventNotification } from "./types";

export interface BridgeOptions {
  /** Stable subscriber identity; recommended form: "vscode:<instanceId>". */
  subscriberId: string;
  /** Subscription filter expression. Defaults to "code.framework". */
  filter?: string;
  /**
   * Optional override for the bound store. Tests inject this via
   * `attachStore` after wiring.
   */
  store?: FrameworkStore;
}

export class DaemonBridge {
  private client: JsonRpcClient | null;
  private store: FrameworkStore | null;
  private filter: string;
  private subscriberId: string;
  private lastCursor = 0;

  constructor(opts: BridgeOptions, client?: JsonRpcClient) {
    this.client = client ?? null;
    this.store = opts.store ?? null;
    this.filter = opts.filter ?? "code.framework";
    this.subscriberId = opts.subscriberId;
  }

  setClient(client: JsonRpcClient): void {
    this.client = client;
  }

  attachStore(store: FrameworkStore): void {
    this.store = store;
  }

  /**
   * Build a ClientOptions block to pass to JsonRpcClient — installs the
   * notification handler + the per-connect handshake that re-issues
   * identify+subscribe with the last-known cursor.
   */
  clientOptions(extra: ClientOptions = {}): ClientOptions {
    return {
      ...extra,
      onConnect: async () => {
        await this.handshake();
        if (extra.onConnect) await extra.onConnect();
      },
      onNotification: (method, params) => {
        if (method === "kernel.event") {
          this.onPush(params as KernelEventNotification);
        }
        extra.onNotification?.(method, params);
      },
    };
  }

  /** Issue identify + subscribe. Idempotent per connection. */
  async handshake(): Promise<void> {
    if (!this.client) throw new Error("daemon bridge: client not set");
    await this.client.call("kernel.identify", { subscriber_id: this.subscriberId });
    const subParams: Record<string, unknown> = {
      filter: this.filter,
      name: `vscode:${this.filter}`,
    };
    if (this.lastCursor > 0) subParams.cursor = this.lastCursor;
    await this.client.call("kernel.subscribe", subParams);
  }

  /**
   * Query function the FrameworkStore uses for cache-fill. Routes per-kind:
   *
   *   - "handler:<name>"           → framework.routes  (the daemon resolves
   *                                  the qualified name to a Route+Handler)
   *   - "event_publisher:<key>"    → framework.event_publishers
   *   - "schema_field:<name>"      → framework.schema_fields
   *
   * Each method accepts {uri, key} and returns {info: EntityLensInfo | null}.
   * Daemon-side these methods are mocked in tests; the production daemon
   * landing in a follow-up commit will register them under the
   * "framework" capability tag.
   */
  buildQuery(): (uri: string, key: string) => Promise<EntityLensInfo | null> {
    return async (uri, key) => {
      const colon = key.indexOf(":");
      const kind = colon > 0 ? key.slice(0, colon) : "";
      const value = colon > 0 ? key.slice(colon + 1) : key;
      const method = methodFor(kind);
      if (!method) return null;
      if (!this.client || !this.client.isConnected()) return null;
      try {
        const res = (await this.client.call(method, { uri, key: value })) as {
          info?: EntityLensInfo;
        } | null;
        return res?.info ?? null;
      } catch {
        return null;
      }
    };
  }

  private onPush(ev: KernelEventNotification): void {
    if (typeof ev?.seq === "number" && ev.seq > this.lastCursor) {
      this.lastCursor = ev.seq;
    }
    this.store?.applyPushEvent(ev);
  }

  /** For tests: read the cursor that reconnect will replay from. */
  cursor(): number {
    return this.lastCursor;
  }
}

function methodFor(kind: string): string | null {
  switch (kind) {
    case "handler":
      return "framework.routes";
    case "event_publisher":
      return "framework.event_publishers";
    case "schema_field":
      return "framework.schema_fields";
    default:
      return null;
  }
}
