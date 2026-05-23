# Go HTTP route extractors (`extractors/route/go/`)

Implements the Go side of P2.T06 + P2.T09 from
`plan/02-framework-extractors.md`. Each subdirectory is one framework
extractor that satisfies the `code_framework.Extractor` contract and
self-registers via `init()` so the orchestrator's blank-import aggregator
picks it up at build time. All four extractors emit `Route` + `Handler`
entities anchored to `code.core` Function entities via the shared
`SelectorRef` shape (SPEC §2.2 — selectors are the only legal
cross-layer reference). `common/` contains the shared content-id /
anchor / event-emission helpers so every Route across the family
collapses to the same `MakeContentID` shape (re-extract over unchanged
source yields identical IDs and the Dispatcher's compare-before-emit
suppresses the re-emission).

## `chi/` — `routes.go.chi`

Detects `github.com/go-chi/chi/v5` route registrations. Supported call
shapes: `r.Get / Post / Put / Patch / Delete / Head / Options / Connect
/ Trace`, the verb-explicit `r.Handle("/path", h)` (emits method
`ANY`), and the verb-positional `r.Method("GET", "/path", h)` /
`r.MethodFunc(...)`. Group prefixes set up via `r.Route("/api", fn)` are
visible as nested call expressions: the extractor recovers the
nested-`r.Get(...)` calls inside the closure but does NOT currently
prepend the group prefix — the prefix joining lands with the Pass-2
pipeline integration when `code.core` group-context tracking is
available. Known limits: dynamic patterns built with `fmt.Sprintf`
slip through (the string-literal anchor only sees literal patterns);
LSP-resolved handler refs (confidence 1.00) are wired in Pass 2.

## `gorillamux/` — `routes.go.gorillamux`

Detects `github.com/gorilla/mux` registrations. Handles the fluent
chain: `r.HandleFunc("/p", h).Methods("GET")`,
`r.Handle("/p", h).Methods("GET","HEAD")`, and the
`r.Path("/p").Methods("POST").HandlerFunc(h)` style — the extractor
unrolls the call chain inside-out and reads the path, handler, and
verb list from whichever step provides each. Method-less chains emit
method `ANY` per gorilla/mux's runtime semantics. Subrouters
(`r.PathPrefix("/api").Subrouter()`) are captured by the inner chains
inside the returned subrouter, but the `/api` prefix is not
automatically prepended to the inner routes (parallel limit to chi
groups). Known limits: schemes / hosts (`r.Schemes("https")` /
`r.Host(...)`) are observed but not modeled in the Route shape;
`.Queries(...)` matchers are ignored.

## `gin/` — `routes.go.gin`

Detects `github.com/gin-gonic/gin` registrations: `r.GET / POST / PUT /
PATCH / DELETE / HEAD / OPTIONS`, `r.Any` (emits method `ANY`),
`r.Handle("CUSTOM", "/path", h)`, and route groups
(`api := r.Group("/api"); api.GET(...)` — the group's prefix is observed
on the `Group` call but not currently prepended to inner routes; this
matches the chi and gorilla policy of deferring prefix joining to the
Pass-2 pipeline). Middleware named arguments between the path and
handler positions (`r.GET("/p", AuthMW, handler)`) are surfaced on
`Route.Middleware` in source order. Anonymous handler closures fall
back to a `path_glob` anchor and report confidence 0.70. Known limits:
middleware chains built dynamically (`r.GET("/p", chain...)`) lose the
expansion; `gin.H` response-shape inference is not implemented.

## `nethttp/` — `routes.go.nethttp`

Detects standard-library route registrations: `http.HandleFunc`,
`http.Handle`, and the equivalent methods on a `*http.ServeMux`
instance (`mux.HandleFunc` / `mux.Handle`). Pre-Go-1.22 patterns
(`"/path"`) emit method `ANY`; Go 1.22+ verb-prefixed patterns
(`"GET /api/orders/{id}"`) are split into (method, path) by checking
the first whitespace-separated token against the closed HTTP verb
list (`GET / POST / PUT / PATCH / DELETE / HEAD / OPTIONS / CONNECT
/ TRACE`); unknown leading tokens are treated as part of the path
(emit `ANY`, keep the full pattern). The extractor accepts any
selector-shaped receiver (`xxx.HandleFunc(...)`) because static
typing of the receiver requires LSP — receiver-type-aware filtering
arrives with the Pass-2 LSP-fallback wiring. Known limits: per-host
mux patterns (`mux.HandleFunc("example.com/path", h)`) are detected
but the host is not split into a separate field; `http.ServeMux` and
the deprecated `http.DefaultServeMux` are not distinguished.
