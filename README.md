<h1 align="center">graph-harness</h1>

<h3 align="center">A graph-anchored substrate runtime <em>for region-scoped harnesses</em></h3>

<p align="center">
  <em>Small, version-controlled rules that pin to a live graph of your code —<br>and attach automatically when humans, CI, or agents touch the region they describe.</em>
</p>

<p align="center">
  <img alt="Status: Early Development" src="https://img.shields.io/badge/status-early%20development-orange.svg">&nbsp;
  <a href="LICENSE"><img alt="MIT License" src="https://img.shields.io/badge/license-MIT-blue.svg"></a>&nbsp;
  <a href="go.mod"><img alt="Go 1.22+" src="https://img.shields.io/badge/go-1.22%2B-00ADD8.svg?logo=go&logoColor=white"></a>&nbsp;
  <a href="docs/concepts/"><img alt="Concepts" src="https://img.shields.io/badge/docs-concepts-blueviolet.svg"></a>&nbsp;
  <a href="CONTRIBUTING.md"><img alt="PRs welcome" src="https://img.shields.io/badge/PRs-welcome-brightgreen.svg"></a>
</p>

<p align="center">
  <a href="#the-layered-graph">Substrate</a> &nbsp;·&nbsp;
  <a href="#what-a-harness-looks-like">Harness</a> &nbsp;·&nbsp;
  <a href="#install">Install</a> &nbsp;·&nbsp;
  <a href="#first-run">First run</a> &nbsp;·&nbsp;
  <a href="#how-it-works">How it works</a> &nbsp;·&nbsp;
  <a href="#features">Features</a> &nbsp;·&nbsp;
  <a href="#documentation">Docs</a>
</p>

---

A repository runs on a graph: source files, symbols, frameworks, and the regions they form. **graph-harness** maintains that graph as a layered, replayable substrate where every fact carries its source and its freshness — and lets you write small, region-scoped artifacts called *harnesses* that pin to it.

When you explore the code, edit a file, open a diff, or run CI, the harnesses anchored in the impacted region attach their context, rules, registered tools, and repair recipes to whoever is operating — a human in code review, CI inside a check, or an agent that reached into your repo.

> [!TIP]
> Harnesses live with the code, in version control, alongside what they govern. One source of truth → three renderings (PR comments, CI gates, MCP responses) that **cannot disagree**.

---

## The layered graph

A repo isn't one graph; it's a stack of related graphs, each with its own freshness, ownership, and source. graph-harness keeps them separate, coordinates them at a single global sequence, and lets harnesses query across them.

<p align="center">
  <img alt="A region-scoped harness pinned across the layered substrate" src="docs/assets/region-scoped-harness.png">
</p>

> [!NOTE]
> A change propagates upward; selectors auto-subscribe to drift events; cached resolutions invalidate lazily. For the model in full, see [concepts/the-layered-graph.md](docs/concepts/the-layered-graph.md).

---

## What a harness looks like

A harness is a small `.gh` file: a selector that pins to a region of the substrate, plus the rules, tools, and repair recipes that attach whenever that region is touched.

```gh
// harnesses/event-payload.gh
// Wakes when an event payload is touched, OR when the impact reaches a
// publisher or consumer (relation-aware: publishes_event, consumes_event).

risk     high
boundary "events"
owners   ["@platform-events"]

selector {
  anchor entity_kind "EventPayload"
  fallback path_glob "services/contracts/**/*.proto"
  thresholds { bound: 0.95; reanchored: 0.75 }
}

rules {
  invariant additive_only {
    statement """ Fields may be added; never removed or retyped. """
    severity blocking
  }
}

verification {
  validate_diff {
    run [semgrep_payload, schema_bump_check]
    check_invariants [additive_only]
    on_violation { mode hard }
  }
}
```

---

## Install

```sh
# From source — requires Go 1.22+ and a C toolchain for tree-sitter
go install github.com/shivamstaq/graph-harness/cmd/graph-harness@latest
```

<details>
<summary><strong>Planned for v0.1</strong> — pre-built binaries via GoReleaser</summary>

<br>

- macOS / Linux (Homebrew) - `brew install graph-harness/tap/graph-harness`
- Windows (Scoop) - `scoop install graph-harness`
- Linux / macOS (script) - `curl -fsSL https://graph-harness.dev/install.sh \- sh`
- Linux (`apt`), version manager (`mise`) - _shipping with v0.1_

</details>

---

## First run

```console
$ cd my-repo
$ graph-harness init
✓ scanned 1,247 files; built source.live, code.core, code.framework
✓ created .graph-harness/{overlay,policies,graph-harness.toml}

$ graph-harness selectors test event-payload
selector event-payload  (harnesses/event-payload.gh)
  outcome:    bound
  matches:    47 entities (12 EventPayload, 18 EventPublisher, 17 EventConsumer)
  resolved_at: kernel_event_seq=4127

$ graph-harness validate-diff --diff main..HEAD
validation_seq=4131. 2 harnesses activated.
  ✗ event-payload   additive_only ✓  schema_versioned ✗  no_pii ✓
  finding_4132 — invariant_violation (blocking)
    OrderCreatedPayload: missing migration under db/event_schema_versions/
    Repair:  bump_schema  (confidence 0.9)
exit 1
```

> [!IMPORTANT]
> **Agent integration.** graph-harness ships an MCP server (`graph-harness mcp`) that any compatible runtime can register as a tool source. The same brief that gates CI is what an agent sees.

### Multi-language quickstart

The same `.gh` flow works across Go, TypeScript, and Python. Each language's
parser materializes a module-prefixed qualified name (Go: `pkg.Class.Method`,
TS: `module.Class.method` from the file basename, Python:
`dotted.path.Class.method`), so a cross-language selector typically combines
a path-glob anchor for the common shape with a body-hash / fingerprint
anchor for rename-survival.

```gh
// .graph-harness/overlay/checkout.gh
selector CheckoutValidator {
  anchor path_glob          "**/checkout/validator.{go,ts,py}"
  anchor symbol_fingerprint "f:validate"
  anchor body_hash          "sha256:..."
}

flow CheckoutValidation {
  description "Pre-payment cart validation, cross-language"
  scope CheckoutValidator
  step ValidateCart targets CheckoutValidator
}
```

```console
$ graph-harness selectors test CheckoutValidator --explain
selector CheckoutValidator
  outcome:    bound  (3 matches across languages)
    1. code.core:Method  language=go      qualified_name=checkout.CheckoutValidator.Validate              via_anchor=path_glob
    2. code.core:Method  language=ts      qualified_name=validator.CheckoutValidator.validate             via_anchor=path_glob
    3. code.core:Method  language=python  qualified_name=checkout.validator.CheckoutValidator.validate    via_anchor=path_glob
  resolved_at: kernel_event_seq=...

$ graph-harness bench --scenario 1 --regime mature --language all
{
  "scenario": { "id": 1, "languages": ["go", "python", "typescript"], ... },
  "per_language": {
    "go":         { "detection_axis": { "score": 1.0, "detected": 1, "expected": 1 }, ... },
    "typescript": { "detection_axis": { "score": 1.0, "detected": 1, "expected": 1 }, ... },
    "python":     { "detection_axis": { "score": 1.0, "detected": 1, "expected": 1 }, ... }
  },
  "aggregate":    { "detection_axis": { "score": 1.0, "detected": 3, "expected": 3 } }
}
```

Bench fixtures for the three-language scenario 1 live under
[`tests/testdata/bench/scenario1/{go,ts,py}/`](tests/testdata/bench/scenario1/);
each variant ships its own `.gh` overlay with a per-language qualified-name
anchor and a real body-hash sha256 (regenerate via
`go run ./cmd/bench-rebake`). The end-to-end multi-language demo automated
by [`tests/smoke/p1/demo.sh`](tests/smoke/p1/demo.sh) (`just smoke`) drives
the same fixtures through `init` → `selectors test` → `validate-diff`
→ `bench` for each language.

Required toolchains for full fact-source coverage (extractors auto-skip
when missing):

| Language | LSP | SCIP indexer | tree-sitter |
|---|---|---|---|
| Go | `gopls` | `scip-go` | bundled |
| TypeScript / JavaScript | `typescript-language-server` | `scip-typescript` | bundled |
| Python | `pyright` | `scip-python` | bundled |

---

## How it works

**1. The substrate stays alive.** As code changes, file events flow into the kernel; layered facts — source structure, calls and refs, framework regions, authored harnesses, change activity, review queue, history — stay coherent at a single global sequence. Every fact carries its source and its freshness; every answer arrives with its provenance.

**2. Operations attach matching harnesses.** Your exploration, an agent's plan, an editor's save, CI's check — graph-harness resolves which entities are *touched* and which are *impacted* via relation-aware rules. The matching harnesses attach their context, rules, registered tools, and repair recipes to whoever is operating.

**3. Operations contribute back.** Every plan, edit, drift detection, and proposal enters the same review queue under the layer's policy. Accepted claims promote into the substrate; rejected ones leave an audit trail. The repo's rules compound — deliberately, not silently.

> For the model in detail, see [Concepts](docs/concepts/).

---

## Features

| | |
|---|---|
| **Region-scoped, refactor-stable selectors** | Multi-anchor with five resolution outcomes (`bound`, `reanchored`, `ambiguous`, `unresolved`, `superseded`) — survives renames, moves, and signature changes where path globs and qualified names rot. |
| **Substrate attaches at every lifecycle moment** | Explore, plan, edit, validate, on-PR, finish — matching harnesses attach their context, rules, registered tools, and repair recipes to whoever is operating. |
| **Same brief for humans, CI, and agents** | Three renderings — PR comments, CI gates, MCP responses — of one source at a pinned sequence. The renderings cannot disagree. |
| **Composes with tools you already use** | Facts come from LSP (live editor state) + SCIP (committed indexer state) + tree-sitter (structural fallback) — three-source unified; disagreements surface as events, not silent merges. Validation invokes Semgrep, ast-grep, custom scripts, and structured agent-review prompts. |
| **Provenance and freshness on every answer** | Folded provenance (confidence, freshness, source classes, contributing inputs) plus constituents accompany every cross-layer response. No unaccompanied claims. |
| **One trust pipeline** | Agent proposals, drift updates, importer outputs, human edits — same review queue under the layer's policy. Promotion is deliberate, not silent. |
| **Open format** | The `.gh` file is a stable, versioned, language-agnostic spec. One Anthropic-format `SKILL.md` ships with the project for compatible agent runtimes. |

---

## Documentation

| Section | What's inside |
|---|---|
| [**Why**](docs/why.md) | The launch essay — context, motivation, design choices, ecosystem observations. |
| [**Concepts**](docs/concepts/) | The substrate (layered graph, single sequence, provenance), selectors, harnesses, lifecycle attachment, three readers, the trust pipeline. |
| [**Comparisons**](docs/comparisons.md) | How graph-harness relates to AGENTS.md, Semgrep, ArchUnit, agent-side rule products, and code-graph backends. |

---

## Community

- **Issues, ideas, roadmap discussions** &mdash; [github.com/shivamstaq/graph-harness](https://github.com/shivamstaq/graph-harness)

---

## Contributing

The fastest places to contribute are listed in [CONTRIBUTING.md](CONTRIBUTING.md). Current call-outs:

- **Language extractors and framework adapters** &mdash; most languages and frameworks are open.
- **Standard harness templates** for new patterns (deprecation, feature-flag boundaries, third-party API gateways, agent-edit drift, …).
- **Importers** from existing rule formats (Semgrep packs, AGENTS.md, ADRs, OpenAPI, GraphQL SDL, CODEOWNERS).
- **Per-runtime install recipes** for the canonical SKILL.md.

---

## License

`graph-harness` is released under the [MIT License](LICENSE)
