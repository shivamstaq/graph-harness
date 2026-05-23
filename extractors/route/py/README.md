# Python HTTP route extractors

This directory hosts the Python-side route extractors for the
`code.framework` layer (SPEC §2.3, plan `02-framework-extractors.md`
P2.T08). One Go package per framework — each registers itself with
`code_framework.Register` during `init()` and emits `RouteAdded` +
`HandlerBound` kernel events when a tracked Python file changes.

## Supported frameworks

| Extractor name | Framework | Recognized pattern |
|---|---|---|
| `routes.py.django`  | Django  | `urlpatterns = [path("…", view), re_path(r"…", view), include("app.urls")]` in any `*urls.py` |
| `routes.py.fastapi` | FastAPI | `@app.get/post/put/delete/patch/head/options/trace("…")` on a FastAPI / APIRouter instance |
| `routes.py.flask`   | Flask   | `@app.route("…", methods=[…])` and `@blueprint.route("…", methods=[…])` |

## How the extractor runs

1. Subscribes to `code.core.FileChanged` events.
2. Filters out non-`.py` paths (Django additionally restricts to
   `*urls.py` files — see Limitations below).
3. Parses the file with the tree-sitter-python grammar (the same
   grammar `internal/source_live/parser_py.go` already uses).
4. Walks decorators / `urlpatterns` list entries; per match emits one
   `ExtractedRoute` carrying method, path pattern, handler qualified
   name, and confidence.
5. The shared `common.ToEvents` helper materializes each
   `ExtractedRoute` into a `(RouteAdded, HandlerBound)` event pair
   bound by selector. The Route's `AnchoredTo` is a
   `qualified_name` SelectorRef pointing at the handler function; the
   dispatcher's `EntityRefCache` resolves it to a `code.core`
   Function row at extract time (P2.T04). Selectors are the only
   cross-layer reference per SPEC §2.2 — no raw `code.core.Entity.ID`
   appears in any emitted payload.

## Confidence rubric

Per `plan/02-framework-extractors.md` §B:

- **0.95 (literal)** — decorator + literal-string path. All FastAPI /
  Flask test fixtures land here.
- **0.85 (computed)** — path or methods=… kwarg is an f-string or
  expression; cross-module view references for Django (`views.foo`).
- **0.70 (dynamic)** — `include("app.urls")` mount points and CBV
  `.as_view()` invocations where the extractor cannot statically
  pinpoint the per-method handler.

## Limitations

### Django (`routes.py.django`)

- The extractor only runs on files whose basename ends in `urls.py`
  (the convention every Django app follows). Routes registered via
  custom URL conf modules with non-standard names are not detected.
- `include("other.urls")` is captured as a single mount-point Route
  with the include target encoded as the handler qualified name; the
  extractor does **not** recursively load the included module.
- Class-based views (`views.AdminPanel.as_view()`) emit one Route
  anchored to the CBV class qualified name. Per-HTTP-method handler
  resolution (the CBV's `get`, `post`, … methods) is left to the
  selector resolver, not this extractor.
- `@require_http_methods([…])` and the per-verb shortcuts
  (`@require_GET`, `@require_POST`) are honored **only** when the view
  function is defined in the same `urls.py` module. Cross-module
  method constraints surface as `ANY`.
- URL `name=` (route names) and `kwargs=` are intentionally not
  surfaced in v1.

### FastAPI (`routes.py.fastapi`)

- Router `prefix=` is honored when the `APIRouter(...)` constructor
  sits in the same module as the decorated handler. Cross-module
  `app.include_router(other, prefix="…")` is **not** chained in v1 —
  the route surfaces with the locally-declared prefix only.
- `@cbv` (class-based-view) decorators from `fastapi-utils` are out
  of scope for v1.
- `response_model=` / `response_class=` are recorded verbatim
  (whatever text appears in the kwarg) so the value may be a Pydantic
  class identifier, not a normalized response shape.

### Flask (`routes.py.flask`)

- `Blueprint(...)` and `Flask(...)` `url_prefix=` is honored when the
  constructor sits in the same module. Cross-module mounting
  (`app.register_blueprint(bp, url_prefix="…")`) is v2 work.
- `MethodView.as_view()` / `add_url_rule(...)` registrations are
  detected only when the assignment site is in the same file. Other
  shapes fall back to confidence 0.7.
- `methods=` with a non-literal value (variable, list comprehension)
  drops confidence to 0.85 and uses the default `['GET']` set.

## Enabling / disabling

The extractors register themselves at package init() time. The
top-level extractors aggregator (`extractors/all/all.go`, owned by
the orchestrator) blank-imports each subdirectory. To disable an
extractor at runtime, use:

```
graph-harness extractors disable routes.py.django
```

To disable at build time, remove the blank-import line from
`extractors/all/all.go`.

## Test fixtures

Each framework subdirectory ships a single `testdata/*.py` fixture
exercising its representative patterns. The Go unit test feeds a
synthetic `code.core.FileChanged` event into `OnEvent` and asserts
the expected Route entities surface.

Run the suite:

```
go test ./extractors/route/py/...
```
