// Wire-shape mirrors of code_framework Go types we need on the lens side.
// Defined as plain TS interfaces because the JSON-RPC envelope is
// language-neutral — anything the daemon emits we just parse with
// `JSON.parse`. Field names match internal/code_framework/types.go.

export type ContentID = string;

export interface Anchor {
  kind: string;
  value: string;
}

export interface SelectorRef {
  anchors: Anchor[];
  unique?: boolean;
}

export interface Route {
  id: ContentID;
  kind: "Route";
  method: string;
  path_pattern: string;
  framework: string;
  middleware?: string[];
  response_kind?: string;
  anchored_to: SelectorRef;
}

export interface Handler {
  id: ContentID;
  kind: "Handler";
  anchored_to: SelectorRef;
}

export interface EventPublisher {
  id: ContentID;
  kind: "EventPublisher";
  event_name: string;
  service?: string;
  transport: string;
  anchored_to: SelectorRef;
}

export interface EventSubscriber {
  id: ContentID;
  kind: "EventSubscriber";
  event_name: string;
  service?: string;
  transport: string;
  anchored_to: SelectorRef;
}

export interface SchemaField {
  id: ContentID;
  kind: "SchemaField";
  schema_id: ContentID;
  name: string;
  data_type: string;
  nullable: boolean;
  anchored_to: SelectorRef;
}

/** Bound-flow / finding summary returned by the daemon for an entity. */
export interface EntityLensInfo {
  /** Number of flows that bind this entity (route / publisher / field). */
  bound_flows?: number;
  /** Number of active findings currently attached to the entity. */
  active_findings?: number;
  /** For EventPublisher only: count of matching EventSubscribers. */
  subscribers?: number;
  /** Optional flow names for the click-through list. */
  flow_names?: string[];
}

/**
 * Wire shape pushed on kernel.event. Mirrors
 * internal/jsonrpc/subscriptions.go::EventNotification.
 */
export interface KernelEventNotification {
  subscription_id: string;
  subscriber_id: string;
  seq: number;
  layer: string;
  kind: string;
  payload?: unknown;
}

/** Event kinds we react to from code.framework — subset of EmittedEventKinds. */
export const REANCHOR_EVENT_KINDS = new Set<string>([
  // Selector reanchored = the qualified-name → entity binding changed,
  // so any lens keyed on that entity needs to re-resolve.
  "selector/reanchored",
  "RouteChanged",
  "HandlerBound",
  "SchemaFieldChanged",
  "EventPublisherAdded",
  "EventPublisherRemoved",
  "EventSubscriberAdded",
  "EventSubscriberRemoved",
  // Flow membership shifts collapse to a re-render of the affected lens.
  "flow/membershipChanged",
  // Invariant violations bump the "N active findings" counter.
  "invariant/violated",
]);
