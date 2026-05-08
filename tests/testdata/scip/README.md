# SCIP wire-format fixtures

`sample.scip` is a binary SCIP `Index` produced by the upstream
`github.com/scip-code/scip/bindings/go/scip` Go encoder against the
fixture defined in `internal/source_live/scip/wire_property_test.go`
(function `upstreamFixture`).

## Why it's committed

Per SPEC §6.17 ("Build-our-own — SCIP wire-format reader rationale"),
the SCIP integration uses a hand-rolled decoder. This static blob is
the canary against silent wire-format drift: even in environments
without network access to the test-only upstream module, our reader
must still decode it successfully.

## Regenerating

If the upstream schema changes shape and the fixture needs refresh,
run from the repository root:

```sh
go test -run TestWireFormat ./internal/source_live/scip/ -update-fixture
```

This re-marshals `upstreamFixture()` via the upstream encoder and
overwrites `sample.scip`. Commit the regenerated blob with a note
linking to the upstream change.

## What it contains

A single `Document` for `pkg/checkout/validator.go` declaring three
symbols (`Validator` struct, `Validator.Validate` method, `Helper`
free function) plus their definition occurrences. Sized to exercise
every SymbolInformation/Occurrence field our reader consumes.
