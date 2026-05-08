// genfixture lives in its own Go module so the renamed upstream
// github.com/scip-code/scip dependency stays out of the main module's
// go.mod. Run from the repository root:
//
//   go run -tags genscipfixture ./internal/source_live/scip/cmd/genfixture
//
// The main go.mod has no import of scip-code/scip; this module is only
// consulted when a maintainer regenerates the SCIP wire-format fixture.
module github.com/shivamstaq/graph-harness/internal/source_live/scip/cmd/genfixture

go 1.26.2

require (
	github.com/scip-code/scip/bindings/go/scip v0.7.1
	google.golang.org/protobuf v1.36.10
)

require github.com/sourcegraph/beaut v0.0.0-20240611013027-627e4c25335a // indirect
