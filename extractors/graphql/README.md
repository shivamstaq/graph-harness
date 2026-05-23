# GraphQL extractors (`code.framework`)

This directory hosts the Phase-2 Pass-1 GraphQL extractor family. Each
(library, language) pair is its own Go package registered via `init()`
under the package-level `code_framework.Register` registry. All
extractors share a single output contract: per-operation
`GraphQLOperation` rows (plus a sibling `Mutation` row for
`operation_type == "mutation"`) anchored at the schema declaration site,
with a `ResolverRef` SelectorRef pointing at the implementing function.

## Registered extractors

| Name | Language | Frameworks | Source files it reads | Detection strategy |
|---|---|---|---|---|
| `graphql.ts.graphqljs` | TypeScript / JavaScript | graphql-js, graphql-tools | `*.ts`, `*.tsx`, `*.js`, `*.jsx`, `*.mjs`, `*.cjs` | `gql`…`` / `graphql`…`` tagged-template SDL **and** resolver-map literals (`Query: { … }` / `Mutation: { … }` / `Subscription: { … }`). |
| `graphql.ts.apollo` | TypeScript / JavaScript | apollo-server, @apollo/server, apollo-server-express | TS/JS like the above | Same SDL + resolver-map shape as graphql-js, gated on the file mentioning an apollo-server import / `ApolloServer` identifier. |
| `graphql.ts.typegraphql` | TypeScript | type-graphql | `*.ts`, `*.tsx` | `@Query()` / `@Mutation()` / `@Subscription()` decorators on class methods inside `@Resolver()` classes. Decorator argument `() => ReturnType` is recovered when present. |
| `graphql.py.strawberry` | Python | strawberry | `*.py` | `@strawberry.field` / `.mutation` / `.subscription` decorators on class methods. `@strawberry.field` operations are classified by the enclosing class name (`Query` / `Mutation` / `Subscription`). |
| `graphql.py.graphene` | Python | graphene | `*.py` | `class Query(graphene.ObjectType): name = graphene.X(...)` field declarations + matching `resolve_<name>` methods, and `class Foo(graphene.Mutation)` subclasses with a `mutate` method. |
| `graphql.py.ariadne` | Python | ariadne | `*.py` | Triple-quoted SDL strings + `QueryType()` / `MutationType()` / `SubscriptionType()` bindings + `@var.field("name")` / `var.set_field("name", resolver)` resolver wiring. |
| `graphql.go.gqlgen` | Go | gqlgen | `*.graphql`, `*.graphqls`, `*.resolvers.go` (and other `*.go` files that look like resolver units) | SDL parsing for schema files; tree-sitter-Go AST walk over method receivers ending in `queryResolver` / `mutationResolver` / `subscriptionResolver` for the generated resolver files. |

Each extractor emits two kernel event kinds:

* `GraphQLOperationAdded` — one per detected operation, with the
  `GraphQLOperation` payload defined in
  `internal/code_framework/types.go`.
* `MutationAdded` — sibling event for operations whose
  `operation_type == "mutation"`, exposing the `Mutation` payload so
  callers that filter by `Kind` do not have to inspect
  `operation_type`.

The Dispatcher (`internal/code_framework/dispatch.go`) handles the
hot-path content-id dedup, provenance stamping (`extractor:framework:<name>`),
and the `RouteAdded` → `RouteChanged` → `RouteRemoved` lifecycle. Pass 1
extractors only emit the `*Added` form; the Dispatcher rewrites the
`Kind` when it diffs against the previous content.

## Anchor shapes

Every operation event carries two `SelectorRef`s:

* `AnchoredTo` — the schema declaration site. Anchors:
  `qualified_name=<operationName>`, `entity_kind=GraphQLOperation`,
  `path_glob=<source path>`.

* `ResolverRef` — the implementing function (i.e. the target of the
  `handles` relation). Anchors:
  `qualified_name=<resolver-qualified-name>`,
  `entity_kind=Function`, `path_glob=<source path>`.

The resolver qualified name follows the convention each library uses:

* graphql-js / Apollo: `Query.<name>` / `Mutation.<name>` /
  `Subscription.<name>` (synthesised from the resolver-map key — the
  module prefix is intentionally omitted so the same selector resolves
  whether the user inlines the resolver map or splits it across files).
* type-graphql: `<ResolverClass>.<methodName>`.
* Strawberry: `<ClassName>.<methodName>`.
* Graphene: `<ClassName>.resolve_<fieldName>` (or `<MutationClass>.mutate`).
* Ariadne: `<resolverFunctionName>` (the bare function identifier).
* gqlgen: `<package>.<ResolverType>.<MethodName>` as produced by the
  `code.core` tree-sitter Go parser (so the SelectorRef lines up 1:1
  with the entity rows in `code_core.entities`).

## Known limitations (v1)

* **Best-effort text scanning.** Most extractors use regular expressions
  + balanced-bracket scanning rather than a full TS/Py AST. This is
  fast and resilient to parse errors (per SPEC §6.11) but loses
  accuracy on novel patterns — e.g.:
  * graphql-js / Apollo: resolver maps nested behind a factory
    function call (`buildResolvers().Query`) are not detected.
  * type-graphql: decorators that span multiple statements (e.g. when
    a user composes a custom decorator factory) may miss the method
    they annotate.
  * Strawberry: when the operation kind cannot be inferred from the
    decorator alone (`@strawberry.field`) the extractor falls back to
    the parent-class name. Classes that follow a different naming
    convention (e.g. `class RootQuery(strawberry.type)`) emit nothing.
* **Cross-file resolution.** Pass-1 extractors only emit per-file.
  Wiring an Ariadne `set_field` to a resolver defined in a *different*
  file produces an event with a `ResolverRef` whose `path_glob` points
  at the schema file rather than the resolver file. The
  `EntityRefCache` (P2.T04) closes the gap at resolve time.
* **No SDL validation.** Invalid SDL is best-effort parsed and surfaces
  whatever fields the regex can recover. Schema linting is out of scope
  for the extractor — `code.framework` consumers may layer validation
  on top.
* **gqlgen.** Identifies methods by the conventional generated receiver
  names (`queryResolver`, `mutationResolver`, `subscriptionResolver`).
  Custom resolver suffixes (`fooQueryResolver`) are not detected. The
  v1 fixture covers the canonical generated layout; broader coverage
  is a follow-up.
* **No support for** Apollo Federation directives, Strawberry
  Federation, Mercurius / Pothos / Nexus / nGraph (TS), Ariadne
  Federation, or schema-stitching across packages. These are
  "supported on contribution" per the §risks row in
  `plan/02-framework-extractors.md`.

## Enabling / disabling

Each extractor is compile-time-activated via blank import in the
aggregator package (`extractors/all/all.go`, owned by the orchestrator).
Runtime enable/disable is via `graph-harness extractors disable
graphql.ts.apollo` (see P2.T05); the Dispatcher honours the flag at
input-event delivery time.

## Adding a new GraphQL library

1. Pick a unique name `graphql.<lang>.<lib>` and create
   `extractors/graphql/<lang>/<lib>/extractor.go`.
2. Implement the `code_framework.Extractor` interface (Name / Inputs /
   Outputs / Capabilities / OnEvent).
3. In `init()`, call `code_framework.Register(name, New, Descriptor{…})`.
4. Add a single-file fixture under `testdata/` and a unit test that
   asserts the operations the fixture should produce.
5. Update this README's table with the new entry and any pattern
   limits.
