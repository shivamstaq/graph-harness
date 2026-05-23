# Test discovery extractors (P2.T24-T27 / P2.T46)

`extractors/test/**` produces three `code.framework` entity kinds:

| Entity | Description |
|---|---|
| `Test` | One test function / sub-case detected in a source file. |
| `Fixture` | A reusable setup/teardown block (pytest `@pytest.fixture`, Jest / Vitest `beforeEach`, `afterEach`, `beforeAll`, `afterAll`). |
| `ContractTest` | A test whose source mentions a topic name or route path that already exists in `code.framework` (Event / Route rows produced by sibling extractors). |

## Supported frameworks

| Extractor | Registry name | Language | Frameworks | File patterns |
|---|---|---|---|---|
| `extractors/test/go/gotest` | `tests.go.gotest` | Go | `testing` package (`go test`) | `*_test.go` |
| `extractors/test/ts/jest` | `tests.ts.jest` | TypeScript / JavaScript | Jest | `*.test.ts(x)`, `*.spec.ts(x)`, `*.test.js(x)`, `*.spec.js(x)` |
| `extractors/test/ts/vitest` | `tests.ts.vitest` | TypeScript / JavaScript | Vitest | same as Jest |
| `extractors/test/py/pytest` | `tests.py.pytest` | Python | pytest | `test_*.py`, `*_test.py` |

Each extractor:
- subscribes to `code.core.FileChanged` (`code_framework.InputCoreFileChanged`);
- reads the changed file from `Deps.Workspace`;
- emits one `TestAdded` per discovered test function;
- emits one `FixtureAdded` per discovered fixture;
- emits one `ContractTestAdded` per literal in the source that matches a known `Event.event_name` or `Route.path_pattern`.

Returning `nil` from `OnEvent` for files that don't match the framework's collection patterns is the normal path — no fact is created and no kernel event is appended.

## Go (`tests.go.gotest`)

Detected patterns:
- `func TestXxx(t *testing.T)` — the standard `go test` shape.
- `t.Run("name", func(t *testing.T) {...})` — sub-tests emit one extra Test row with name `TestXxx/name`.
- Iterated table-driven tests: when `t.Run` is called with a struct-field reference (e.g. `t.Run(tc.name, ...)`), the extractor scans the enclosing function body for `name: "..."` struct-literal entries and uses each as a sub-case.

Limitations:
- `testing/quick`, `testify` suites, `subtests.Bench`, fuzz tests (`func FuzzXxx(f *testing.F)`) are not detected in v1.
- Sub-case names that come from a function call (not a literal or struct field) are not recovered — those tests still produce the parent Test row.

## TypeScript (`tests.ts.jest`, `tests.ts.vitest`)

Both extractors share the same call-shape scanner: `describe(...)`, `it(...)`, `test(...)`, `beforeEach(...)`, `afterEach(...)`, `beforeAll(...)`, `afterAll(...)`. Modifier chains (`.only`, `.skip`, `.each(...)`, `.concurrent`, `.sequential`) are tolerated.

The `describe` stack is reconstructed by brace-balance so nested describes produce slash-joined `Test.Name` values (`OrderService/cancel/cancels happy path`).

Disambiguation between Jest and Vitest:
- a file is treated as **Vitest** iff it contains `from 'vitest'` (or `"vitest"` / `vitest/globals`);
- otherwise it is treated as **Jest** (the Jest globals are typically implicit through `@types/jest`).

Limitations:
- Titles built from template-literal interpolations (`it(`${prefix} works`, ...)`) keep the call site but fall back to `<computed>` for the name. Subject inference still runs against the file body.
- JSX-heavy `.tsx` test files parse with the same regex; only the title-extraction is grammar-sensitive, and JSX expressions inside string positions are rare.

## Python (`tests.py.pytest`)

Detected patterns:
- `def test_xxx(...)` at any indent level (collected from both module-level functions and methods of `TestClass`-style classes).
- `@pytest.fixture` (or `@fixture` when imported directly) — supports decorator args (`scope="module"` etc.) but does not yet surface scope as a Fixture attribute.

Limitations:
- `@pytest.mark.parametrize` does not produce per-tuple Test rows in v1 — one Test row per `def` regardless of how many test invocations pytest will collect. Documented in `plan/02-framework-extractors.md` §5 risks.
- `unittest.TestCase` subclasses (`class FooTest(unittest.TestCase): def test_x(...)`) are picked up because the `def test_x` regex matches at any indent level; the discrimination between pytest and unittest mode is handled by pytest itself at run time.

## Subject inference

Each `Test.SubjectRef` is best-effort — the plan §5 risk row documents the inherent ambiguity. The heuristic:

1. Strip the language-specific prefix from the test name (`Test` for Go, `test_` for Python, the inner-`it` title for TS).
2. Scan the test source for qualified names (`pkg.Func`, `Class.method`, `module.func`).
3. For each candidate, compute a similarity score against the stripped test name:
   - 1.0 — exact match (case-insensitive).
   - 0.8 — prefix / suffix match.
   - 0.6 — substring match in either direction.
4. Pick the best match with score ≥ 0.5; emit a `SelectorRef` anchored at `qualified_name = <match>` with confidence in **0.6..0.8** (mapped from similarity 0.5..1.0).
5. On miss, fall back to PascalCase identifiers in the file — confidence capped at 0.6.

Confidence stays medium by design. Downstream code is expected to treat the inference as a hint, not a binding contract. Studio / TUI surfaces label the score so users can override.

## Contract test heuristic

`ContractTest` rows close the loop between Tests and the Routes / Events sibling extractors produce. The matcher:

1. Queries `Deps.Facts.ReadCurrent` for the current `code.framework` state.
2. Harvests every `Event.name` (from `EventTopicObserved` / `EventPublisherAdded` / `EventSubscriberAdded` payloads) and every `Route.path_pattern` (from `RouteAdded` / `RouteChanged` payloads).
3. Scans the test source's string literals for exact matches against either set.
4. Emits one `ContractTest` per `(file, literal)` match.

If the `code.framework` store has no Routes / Events yet (Pass 1 of Phase 2 spins extractors up in parallel), the matcher returns no matches and no `ContractTest` is emitted. The extractor never fails on a missing-rows lookup — the worst case is delayed enrichment, which lands on the next `FileChanged` for the test file (re-extract on edit).

Topic-name matching is string-equality only — typed event registries (CloudEvents, AsyncAPI) are post-v1 per plan §5 risks. Workspaces with topic-name reuse across unrelated contracts will see one `ContractTest` row per match per test file; downstream consumers can deduplicate by `(topic, path)`.

## Limitations summary (rolled up)

- **Subject inference**: medium confidence by design. No flow-graph reasoning yet; the resolver picks the best lexical match in the file.
- **Sub-test recovery**: literal-string `t.Run` + the canonical `name: "..."` table idiom. Other table shapes (string-slice loops, map iteration) are not recovered.
- **Parametrize**: pytest `@pytest.mark.parametrize` is not expanded.
- **Fixture scope**: not captured as a fact attribute in v1.
- **Suite tests** (`testify`, `unittest.TestCase`): top-level functions are still detected; the suite grouping is not.
- **Contract matching**: string equality against `Event.event_name` and `Route.path_pattern`. Cross-language topic-name fragility is documented in `extractors/events/README.md` (sibling extractor).

## Registry side effects

Each extractor's package `init()` calls `code_framework.Register`. The orchestrator's aggregator (one level up) blank-imports each subdirectory; disabling an extractor at build time removes its blank import line. Runtime disable goes through `Dispatcher.Disable("tests.<lang>.<framework>")`.
