# SCIP wire-format fixtures

`sample.scip` is a binary SCIP `Index` produced by the upstream
Sourcegraph SCIP Go encoder
(`github.com/scip-code/scip/bindings/go/scip` v0.7.1) on a hand-built
fixture. The blob is committed so our hand-rolled wire reader can be
exercised in CI without any third-party encoder running.

## Why it's committed

Per SPEC §6.17 ("Build-our-own — SCIP wire-format reader rationale"),
the SCIP integration uses a hand-rolled decoder. This blob is the
canary against silent wire-format drift: a real third-party encoder
produced these bytes; if our reader can't decode them, the upstream
wire format has shifted under us.

## Encoder lockfile

| Field | Value |
|---|---|
| Encoder module | `github.com/scip-code/scip/bindings/go/scip` |
| Encoder version | `v0.7.1` |
| Generator | `internal/source_live/scip/cmd/genfixture/main.go` |
| Bytes | 458 |

The generator lives in **its own Go module** (`internal/source_live/scip/cmd/genfixture/go.mod`)
so the renamed upstream dependency stays out of the main module's
`go.mod` and out of every `go test ./...` run.

## Regenerating

If the upstream schema changes shape and the fixture needs refresh:

```sh
cd internal/source_live/scip/cmd/genfixture
go run -tags genscipfixture .
```

The tool writes the new blob to `tests/testdata/scip/sample.scip`,
prints the encoder version it observed, and reminds you to update the
"Encoder lockfile" table above if the version shifted.

To bump the encoder pin first:

```sh
cd internal/source_live/scip/cmd/genfixture
go get github.com/scip-code/scip/bindings/go/scip@<new-version>
go mod tidy
```

## What the fixture contains

A single `Document` for `pkg/checkout/validator.go` declaring three
symbols and their definition occurrences:

| Symbol | Kind | Range |
|---|---|---|
| `pkg/checkout.Validator` | Struct | — |
| `pkg/checkout.Validator.Validate` | Method | `[3,0,5,4]` |
| `pkg/checkout.Helper` | Function | `[8,0,10,4]` |

Sized to exercise every SymbolInformation / Occurrence field our
reader consumes (kind, display_name, packed-int32 range, role bits)
plus the per-language importer's qualified-name reconstruction.
