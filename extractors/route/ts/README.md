# routes.ts — TypeScript HTTP route extractors

Pass-1 framework extractors that produce `code.framework` `Route` +
`Handler` entities from TypeScript source files. One subpackage per
recognized framework:

| Extractor name        | Subdir     | Patterns covered                                                          |
| --------------------- | ---------- | ------------------------------------------------------------------------- |
| `routes.ts.express`   | `express/` | `app.<method>(path, handler)`, sub-routers, `app.use('/api', router)`     |
| `routes.ts.fastify`   | `fastify/` | `fastify.<method>(...)`, `fastify.route({...})`, `fastify.register(...)`  |

Both subpackages register themselves with `code_framework.Register`
in their `init()` so a blank import (the daemon's
`extractors/all/all.go` aggregator does this in Pass 1) is enough
to activate them.

## Contract

Each extractor:

* Implements `code_framework.Extractor`.
* Subscribes to `code.core.FileChanged` only.
* Emits `KindRoute` and `KindHandler` entities, wrapped as
  `RouteAdded` / `HandlerBound` `kernel.Event`s. The Dispatcher
  stamps `Layer` (`code.framework`), `ProducedBy`
  (`extractor:framework:<name>`), `Tx`, and `Causes` before the
  events land in the EventLog.
* Builds entity ids with `code_framework.MakeContentID` so identical
  re-parses collapse via compare-before-emit (§6.21).

## Selector strategy

Routes anchor their handlers with a `SelectorRef` whose preferred
anchor is `qualified_name` and whose fallback is `path_glob`:

```text
Anchors:
  - qualified_name = <handler identifier or member path>
  - path_glob      = <source file path>            (always present)
  - body_hash      = <path:start-end byte span>    (inline handlers)
```

When the handler is an inline arrow / function expression, no
`qualified_name` is emitted; the `body_hash` anchor uniquely
identifies the inline span so two inline handlers in the same file
do not collide.

## Confidence rubric

`Provenance.Confidence` is set from the path-literal source:

| Source           | Example                              | Confidence |
| ---------------- | ------------------------------------ | ---------- |
| `string`         | `app.get('/users', ...)`             | 0.95       |
| `template`       | `app.get(\`/v2/widgets\`, ...)`      | 0.95       |
| `computed`       | `app.get(\`/v2/${kind}\`, ...)`      | 0.85       |
| `dynamic`        | `app.get(routes.list, ...)`          | 0.60       |

A dynamic path is still emitted — downstream consumers can decide
how to surface low-confidence routes; we never drop signal at the
source.

## Fixtures

`<framework>/testdata/app.ts` is a hand-crafted TS file exercising
the major call shapes for the framework. The corresponding
`extractor_test.go` synthesizes a `code.core.FileChanged` event with
`payload.path = "testdata/app.ts"`, runs `OnEvent`, and asserts the
expected `(method, path)` multiset and per-route anchor shape.

## Gates

* `go build ./extractors/route/ts/...`
* `go test ./extractors/route/ts/...`
* `go vet ./extractors/route/ts/...`

## Sibling extractors

* `routes.go.*` — owned by 1-Routes-Go (`extractors/route/go/**`).
* `routes.py.*` — owned by 1-Routes-Py (`extractors/route/py/**`).

All three families share `code_framework.Route` and emit the same
`RouteAdded` / `HandlerBound` event kinds; downstream consumers do
not distinguish between languages.
