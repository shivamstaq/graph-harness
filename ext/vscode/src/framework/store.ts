// FrameworkStore caches lens-render data and converts push events into
// "this URI needs re-rendering" signals. It is the boundary between
// the JSON-RPC client and the VS Code-facing CodeLensProvider.
//
// Cache shape: per-document Map<"kind:key", EntityLensInfo>.
// A re-anchor / membership-change / invariant-violation push event
// invalidates the affected (uri, key) and triggers `onChanged` for that
// uri. Callers re-query through `lookup`, which lazy-fills the cache
// via the injected `query` function (the JSON-RPC client wrapper).

import { EventEmitter } from "events";
import type { EntityLensInfo, KernelEventNotification } from "./types";
import { REANCHOR_EVENT_KINDS } from "./types";

export type EntityKey = string; // "handler:Foo" | "event_publisher:kafka.NewWriter" | "schema_field:email"

export interface QueryFn {
  (uri: string, key: EntityKey): Promise<EntityLensInfo | null>;
}

/** Extracts entity key(s) from a push-event payload. */
export type AffectedKeysExtractor = (ev: KernelEventNotification) => Array<{ uri: string; key: EntityKey }>;

export interface StoreOptions {
  query: QueryFn;
  /**
   * Maps a kernel.event notification onto the (uri, key) pairs it
   * invalidates. The default extractor pulls `uri` and `key` from
   * `ev.payload` if present — daemon-side helpers typically attach those
   * fields when emitting reanchor / membership events.
   */
  extractAffected?: AffectedKeysExtractor;
}

const defaultExtractor: AffectedKeysExtractor = (ev) => {
  if (!ev || !ev.payload || typeof ev.payload !== "object") return [];
  const p = ev.payload as Record<string, unknown>;
  const uri = typeof p.uri === "string" ? p.uri : typeof p.path === "string" ? p.path : undefined;
  const keys = Array.isArray(p.keys) ? (p.keys.filter((k): k is string => typeof k === "string"))
    : typeof p.key === "string" ? [p.key]
    : [];
  if (!uri || keys.length === 0) {
    // If the event carries no specific anchor, treat it as a broad
    // invalidation — caller can pass uri === "*" to mean "drop all".
    return uri ? [{ uri, key: "*" }] : [{ uri: "*", key: "*" }];
  }
  return keys.map((k) => ({ uri, key: k }));
};

export class FrameworkStore {
  private readonly cache = new Map<string, Map<EntityKey, EntityLensInfo | null>>();
  private readonly emitter = new EventEmitter();
  private readonly query: QueryFn;
  private readonly extract: AffectedKeysExtractor;

  constructor(opts: StoreOptions) {
    this.query = opts.query;
    this.extract = opts.extractAffected ?? defaultExtractor;
  }

  /**
   * Get lens info for (uri, key). Returns the cached value if present;
   * otherwise fires `query`, caches the result, and returns it.
   */
  async lookup(uri: string, key: EntityKey): Promise<EntityLensInfo | null> {
    const docCache = this.cache.get(uri);
    if (docCache && docCache.has(key)) {
      return docCache.get(key) ?? null;
    }
    const info = await this.query(uri, key);
    this.put(uri, key, info);
    return info;
  }

  private put(uri: string, key: EntityKey, info: EntityLensInfo | null): void {
    let docCache = this.cache.get(uri);
    if (!docCache) {
      docCache = new Map();
      this.cache.set(uri, docCache);
    }
    docCache.set(key, info);
  }

  /**
   * Drop the cached entry for (uri, key). Pass uri === "*" or key === "*"
   * to broaden the invalidation. After invalidation, callers should
   * re-query via `lookup`.
   */
  invalidate(uri: string, key: EntityKey): void {
    if (uri === "*") {
      this.cache.clear();
      return;
    }
    const docCache = this.cache.get(uri);
    if (!docCache) return;
    if (key === "*") {
      this.cache.delete(uri);
      return;
    }
    docCache.delete(key);
    if (docCache.size === 0) this.cache.delete(uri);
  }

  /**
   * Subscribe to "this uri's lenses became stale" notifications. The
   * callback fires once per uri per push event (deduped within a single
   * dispatch), with `"*"` when an event lacks a uri.
   */
  onChanged(listener: (uri: string) => void): { dispose(): void } {
    this.emitter.on("changed", listener);
    return {
      dispose: () => this.emitter.off("changed", listener),
    };
  }

  /**
   * Apply a server-pushed kernel.event notification. Returns the set of
   * URIs whose lenses must re-render (also fires `changed` events for
   * them). Events outside REANCHOR_EVENT_KINDS are ignored.
   */
  applyPushEvent(ev: KernelEventNotification): Set<string> {
    if (!REANCHOR_EVENT_KINDS.has(ev.kind)) return new Set();
    const affected = this.extract(ev);
    const uris = new Set<string>();
    for (const { uri, key } of affected) {
      this.invalidate(uri, key);
      uris.add(uri);
    }
    for (const uri of uris) {
      this.emitter.emit("changed", uri);
    }
    return uris;
  }
}
