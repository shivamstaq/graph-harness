# Graph Harness — VS Code extension

The official Graph Harness VS Code extension. Talks to the workspace daemon
over the same socket / named pipe the CLI uses (SPEC §9.4) — no second
protocol.

## Features

- **Code lens** on `.gh` declarations: hovering over a `selector NAME { ... }`
  block shows the live match count returned by `selectors.test` over
  JSON-RPC.
- **Problems pane**: `change.process` findings hydrate into VS Code's
  Problems pane via the `graph-harness` diagnostic source.
- **Cross-language activation** (P1): the extension activates on `.go`,
  `.ts`, `.tsx`, `.js`, `.jsx`, and `.py` so the multi-LSP host and
  unifier surface findings regardless of which language was edited.
- **Syntax highlighting** for `.gh` files via a TextMate grammar
  (`syntaxes/gh.tmLanguage.json`). Full LSP-for-DSL is deferred (P2+).

## Build

```bash
npm install
npm run compile
npx vsce package      # produces graph-harness-X.Y.Z.vsix
code --install-extension graph-harness-X.Y.Z.vsix
```

The extension shells out to the `graph-harness` CLI on the user's `$PATH` for
RPC calls; the CLI itself proxies to the daemon. Marketplace publishing is
deferred to P5/P6 (plan §5 risks table).
