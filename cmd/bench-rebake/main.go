// Command bench-rebake recomputes body_hash sentinel values in the
// scenario 1 polyglot bench fixtures. Run after editing the .go / .ts /
// .py source files in tests/testdata/bench/scenario1/{go,ts,py}/ to
// keep the body_hash anchor in each variant's overlay/checkout.gh
// in sync with the parser's tree-sitter-extracted body bytes.
//
// Usage:
//
//	go run ./cmd/bench-rebake [path/to/scenario-root]
//
// Defaults to tests/testdata/bench/scenario1/ relative to cwd.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/shivamstaq/graph-harness/internal/source_live"
)

func main() {
	root := "tests/testdata/bench/scenario1"
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	if os.Getenv("INSPECT") == "1" {
		for _, lang := range []string{"go", "ts", "py"} {
			fmt.Println("---", lang)
			inspectParsedFiles(filepath.Join(root, lang))
		}
		return
	}

	type target struct {
		variant       string
		sourceFile    string
		overlayFile   string
		qualifiedName string
	}
	targets := []target{
		{"go", "go/internal/checkout/validator.go", "go/.graph-harness/overlay/checkout.gh", "checkout.CheckoutValidator.Validate"},
		{"ts", "ts/src/checkout/validator.ts", "ts/.graph-harness/overlay/checkout.gh", "validator.CheckoutValidator.validate"},
		{"py", "py/checkout/validator.py", "py/.graph-harness/overlay/checkout.gh", "checkout.validator.CheckoutValidator.validate"},
	}

	for _, t := range targets {
		src, err := os.ReadFile(filepath.Join(root, t.sourceFile)) //nolint:gosec
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", t.variant, err)
			continue
		}
		pf, err := source_live.ParseFile(t.sourceFile, src)
		if err != nil || pf == nil {
			fmt.Fprintf(os.Stderr, "skip %s: parse failed: %v\n", t.variant, err)
			continue
		}
		hash := ""
		for _, fn := range pf.Functions {
			if strings.EqualFold(fn.QualifiedName, t.qualifiedName) ||
				strings.HasSuffix(fn.QualifiedName, "."+t.qualifiedName) {
				hash = fn.BodyHash
				break
			}
		}
		if hash == "" {
			fmt.Fprintf(os.Stderr, "%s: qualified_name %q not found in parsed file\n", t.variant, t.qualifiedName)
			continue
		}
		overlayPath := filepath.Join(root, t.overlayFile)
		raw, err := os.ReadFile(overlayPath) //nolint:gosec
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: read overlay: %v\n", t.variant, err)
			continue
		}
		// Replace the body_hash anchor value (sentinel or stale).
		re := regexp.MustCompile(`(anchor body_hash )"[^"]*"`)
		updated := re.ReplaceAllString(string(raw), `$1"sha256:`+hash+`"`)
		if updated == string(raw) {
			fmt.Printf("%s: body_hash already up to date (%s)\n", t.variant, hash[:12])
			continue
		}
		if err := os.WriteFile(overlayPath, []byte(updated), 0o600); err != nil { //nolint:gosec // operator-supplied path inside fixture root
			fmt.Fprintf(os.Stderr, "%s: write overlay: %v\n", t.variant, err)
			continue
		}
		fmt.Printf("%s: body_hash → sha256:%s\n", t.variant, hash[:12]+"...")
	}
}
