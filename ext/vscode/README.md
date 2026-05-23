# Graph Harness — VS Code extension

The official Graph Harness VS Code extension. Talks to the workspace daemon
over the same socket / named pipe the CLI uses (SPEC §9.4) — no second
protocol.

## Features

- **Code lens** on `.gh` declarations: hovering over a `selector NAME { ... }`
  block shows the live match count returned by `selectors.test` over
  JSON-RPC.
- **Framework code lens** (P2.T40): "N bound flows · N active findings"
  (and "N subscribers" for event publishers) above HTTP route handlers,
  event-publisher call sites, and schema-field declarations
  (Prisma / Drizzle / SQLAlchemy / GORM).
- **Push-driven re-render** (P2.T40a): the extension subscribes to
  `code.framework` drift events over the daemon's long-lived JSON-RPC
  channel and re-renders the affected lenses without polling.
- **Problems pane**: `change.process` findings hydrate into VS Code's
  Problems pane via the `graph-harness` diagnostic source.
- **Cross-language activation** (P1): the extension activates on `.go`,
  `.ts`, `.tsx`, `.js`, `.jsx`, `.py`, and `.prisma` so the multi-LSP
  host and unifier surface findings regardless of which language was
  edited.
- **Syntax highlighting** for `.gh` files via a TextMate grammar
  (`syntaxes/gh.tmLanguage.json`). Full LSP-for-DSL is deferred (P2+).

## Build

```bash
npm install
npm run compile
npx vsce package      # produces graph-harness-X.Y.Z.vsix
code --install-extension graph-harness-X.Y.Z.vsix
```

The extension shells out to the `graph-harness` CLI on the user's `$PATH`
for the legacy `.gh` lens + diff diagnostics; for the framework lens it
dials the daemon socket directly so push events round-trip without a
per-event CLI exec. Marketplace publishing is deferred to P5/P6 (plan §5
risks table).

## Test

```bash
npm test            # node:test via tsx (no editor host required)
npm run test:types  # strict type-check of the test sources
```

The suite spans unit tests for the code-lens detectors / store / framing
and an integration test that wires the `JsonRpcClient` to an in-memory
mock daemon and asserts the push → re-render path.

---

## Framework code lens (P2.T40)

Lenses are attached above source-level declarations that the daemon may
have bound to a `code.framework` entity:

| Detected site | Daemon RPC | Lens contents |
|---|---|---|
| Function in `handlers/`, `routes/`, `controllers/`, `api/`, `endpoints/` | `framework.routes` | bound flows · active findings |
| `kafka.NewWriter`, `producer.send(...)`, `nc.publish(...)`, `channel.publish(...)`, `Producer(...)`, etc. | `framework.event_publishers` | bound flows · active findings · subscribers |
| Prisma `model { … }` fields, Drizzle `pgTable` columns, SQLAlchemy `Column`/`mapped_column`, GORM-tagged Go struct fields | `framework.schema_fields` | bound flows · active findings |

Per-entity RPC payload shape:

```jsonc
// request
{ "uri": "file:///repo/internal/handlers/users.go", "key": "ListUsers" }
// response
{ "info": { "bound_flows": 2, "active_findings": 1, "flow_names": ["UserSignup", "Onboarding"] } }
```

Detection is intentionally regex-level and language-aware: false
positives just produce an empty-binding lens; the daemon stays the
authoritative resolver of "is this *actually* a route handler?".

---

## Push subscription contract (P2.T40a)

The extension does **not poll**. On activation it opens a single
JSON-RPC 2.0 connection (Content-Length framing) to the workspace
daemon's socket — `internal/daemon/workspace.go::socketPath` — and
performs the SPEC §6.22 long-lived-subscriber handshake:

```text
→ kernel.identify   { "subscriber_id": "vscode:<extensionInstanceId>" }
← kernel.identify   { "subscriber_id": "vscode:<extensionInstanceId>" }

→ kernel.subscribe  { "filter": "code.framework",
                      "name":   "vscode:code.framework",
                      "cursor": <lastSeenSeq | omitted on first connect> }
← kernel.subscribe  { "subscription_id": "sub-…", "subscriber_id": "vscode:…" }

… server-pushed notifications:
← kernel.event      { "subscription_id": "sub-…",
                      "subscriber_id":   "vscode:…",
                      "seq": 17,
                      "layer": "code.framework",
                      "kind":  "selector/reanchored" | "flow/membershipChanged" | "invariant/violated" | …,
                      "payload": { "uri": "file:///…",
                                   "key": "handler:…" | "schema_field:…" | "event_publisher:…" } }
```

### Cursor + reconnect

- `extensionInstanceId` is allocated once and persisted in
  `ctx.workspaceState`; the same id is reused across reloads so the
  daemon's persisted `(subscriber_id, subscription_name)` cursor lines
  up across restarts (SPEC §6.22 + F8).
- Every `kernel.event` advances an in-memory `lastSeenSeq`.
- On socket disconnect the client reconnects with **exponential
  backoff** (500ms → 30s cap, doubling per failure) and re-issues
  `kernel.identify` + `kernel.subscribe { cursor: lastSeenSeq }`.
- `kernel.ack` is not issued by the extension surface — the
  edit-cycle latency budget (sub-100ms re-render) makes the per-event
  RPC round-trip wasteful. The reconnect cursor is the recovery anchor.

### Re-render triggers

The lens provider fires `onDidChangeCodeLenses` (which VS Code answers
with another `provideCodeLenses`/`resolveCodeLens` pair) whenever the
`FrameworkStore` invalidates a URI. Today the store treats these
`kernel.event` kinds as re-render signals:

| Kind | Meaning |
|---|---|
| `selector/reanchored` | The qualified-name → entity binding moved; any lens on that file is stale. |
| `flow/membershipChanged` | A flow set this entity into / out of its membership; recount. |
| `invariant/violated` | A new finding attached to this entity; bump the count. |
| `RouteChanged`, `HandlerBound`, `SchemaFieldChanged`, `EventPublisher{Added,Removed}`, `EventSubscriber{Added,Removed}` | Framework entity inventory drift; recount. |

Other kinds (`RouteAdded`, `SchemaAdded`, …) are intentionally ignored
to avoid a flood of re-renders during a cold extractor pass.

### Scoping

The lens provider scopes its `framework.*` queries to URIs the user
currently has open — VS Code only calls `provideCodeLenses` /
`resolveCodeLens` for visible documents, so subscriptions are not
narrowed at the daemon side (the `code.framework` filter is coarse).
This keeps the extension out of the business of inferring which
qualified names live in each open buffer; the store caches per
`(uri, key)` and lazy-fills on first resolve.

---

## Module layout

```
src/
  extension.ts                 # activation, code-lens + bridge wiring
  rpc/
    socketPath.ts              # workspace → socket path (mirrors Go)
    framing.ts                 # Content-Length frame reader/encoder
    client.ts                  # JSON-RPC 2.0 client w/ auto-reconnect
  framework/
    types.ts                   # wire shapes (Route, Handler, …)
    detectors.ts               # source → (kind, line, key) detection
    store.ts                   # lens-info cache + push-event router
    lensProvider.ts            # vscode.CodeLensProvider
    daemonBridge.ts            # binds the client + store together
  test/                        # node:test suites (tsx + _vscode-mock)
```
