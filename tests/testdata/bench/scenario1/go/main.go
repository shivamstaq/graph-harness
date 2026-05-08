// Command checkout-svc is the trivial host that consumes
// CheckoutValidator. Used by the bench fixture to give the call graph a
// callsite the BFS-based impacted_set can fold over.
package main

import (
	"fmt"
	"os"

	"checkout-svc/internal/checkout"
)

func main() {
	v := &checkout.CheckoutValidator{}
	if err := v.Validate(map[string]any{"id": 1}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("ok")
}
