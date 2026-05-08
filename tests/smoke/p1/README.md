# Phase-1 smoke

`tests/smoke/p1/demo.sh` automates the multi-language demo from
`plan/01-multi-language-three-source.md` §4 end-to-end:

1. Initialize a polyglot workspace (Go + TS + Python).
2. Author one cross-language `.gh` selector + flow.
3. Verify the selector resolves in all three languages.
4. Apply the unreviewed target diff to each language variant.
5. Run `validate-diff` against each → expect a `flow_unreviewed`
   finding per language.
6. Run `bench --scenario 1 --regime mature` → expect `detection_axis`
   score of 1.0 per language.

The script is the source of truth `just smoke` invokes once the phase 1
gotit wave is open. Until then it can be run directly:

```bash
bash tests/smoke/p1/demo.sh
```

The script tolerates missing language toolchains (gopls / tsserver /
pyright / scip-go / scip-typescript / scip-python) by skipping the
fact-source check that requires them and reporting via exit code 77 —
matching the gotit "skipped (gated)" convention.
