package helpers

import (
	"path/filepath"

	"github.com/shivamstaq/gotit/runner"
)

// GoModuleEmpty creates a workspace with `go.mod` only. Use for `init` and
// daemon-lifecycle specs that don't care about source code.
func GoModuleEmpty(_, workDir string, _ map[string]any) error {
	if err := runner.WriteFile(
		filepath.Join(workDir, "go.mod"),
		"module example.com/empty\n\ngo 1.23\n",
	); err != nil {
		return err
	}
	if err := runner.GitInit(workDir); err != nil {
		return err
	}
	return runner.GitCommitAll(workDir, "initial empty go module")
}

// GoModuleWithCheckoutValidator creates a small Go module containing a
// CheckoutValidator type with a Validate method, plus a callsite, plus the
// canonical .gh overlay flow that the tracer demo exercises. This is the
// Phase-0 demo fixture.
func GoModuleWithCheckoutValidator(_, workDir string, _ map[string]any) error {
	overlayGH := `selector CheckoutValidator {
  unique
  anchor qualified_name "checkout.CheckoutValidator.Validate"
}

flow CheckoutValidation {
  description "Pre-payment cart validation"
  scope CheckoutValidator
  step ValidateCart targets selector { qualified_name "checkout.CheckoutValidator.Validate" }
}
`
	diff := `--- a/internal/checkout/validator.go
+++ b/internal/checkout/validator.go
@@ -3,5 +3,7 @@ package checkout
 type CheckoutValidator struct{}

-func (v *CheckoutValidator) Validate(cart any) error {
+// Validate runs the cart validation pipeline (now with extra logging).
+func (v *CheckoutValidator) Validate(cart any) error {
+	_ = cart
 	return nil
 }
`
	files := map[string]string{
		"go.mod": "module example.com/checkout\n\ngo 1.23\n",
		"internal/checkout/validator.go": `package checkout

// CheckoutValidator validates pre-payment cart state.
type CheckoutValidator struct{}

// Validate runs the cart validation pipeline.
func (v *CheckoutValidator) Validate(cart any) error {
	if cart == nil {
		return nil
	}
	return nil
}
`,
		"main.go": `package main

import (
	"example.com/checkout/internal/checkout"
)

func main() {
	v := &checkout.CheckoutValidator{}
	_ = v.Validate(nil)
}
`,
	}
	for relPath, content := range files {
		if err := runner.WriteFile(filepath.Join(workDir, relPath), content); err != nil {
			return err
		}
	}
	if err := runner.GitInit(workDir); err != nil {
		return err
	}
	if err := runner.GitCommitAll(workDir, "initial checkout fixture"); err != nil {
		return err
	}
	// Pre-stage the demo .gh overlay AND the target diff in the workspace
	// (outside .graph-harness/, which `init` will create). The spec consumes
	// these without needing heredoc escapes in YAML.
	if err := runner.WriteFile(filepath.Join(workDir, "demo.checkout.gh"), overlayGH); err != nil {
		return err
	}
	return runner.WriteFile(filepath.Join(workDir, "demo.diff"), diff)
}

// TSModuleWithCheckoutValidator creates a TypeScript / Express checkout
// service with a CheckoutValidator class whose `validate` method is
// flow-scoped. Mirrors GoModuleWithCheckoutValidator's shape so the same
// `.gh` flow exercises the TS variant. Used by phase1 specs.
func TSModuleWithCheckoutValidator(_, workDir string, _ map[string]any) error {
	overlayGH := `selector CheckoutValidator {
  unique
  anchor qualified_name "validator.CheckoutValidator.validate"
  anchor symbol_fingerprint "f:validate/sig=Cart:void"
}

flow CheckoutValidation {
  description "Pre-payment cart validation (TypeScript)"
  scope CheckoutValidator
  step ValidateCart targets selector { qualified_name "validator.CheckoutValidator.validate" }
}
`
	diff := `--- a/src/checkout/validator.ts
+++ b/src/checkout/validator.ts
@@ -10,9 +10,11 @@ export type Cart = {
 export class CheckoutValidator {
   /** Runs the cart validation pipeline. Throws if any invariant fails. */
   validate(cart: Cart): void {
+    // unreviewed edit: extra logging for cart sanity
     if (!cart) throw new Error("cart is required");
     if (!Array.isArray(cart.items)) throw new Error("items must be an array");
     for (const item of cart.items) {
+      // unreviewed: assert positive quantity once more
       if (item.quantity <= 0) throw new Error("quantity must be positive");
     }
   }
`
	files := map[string]string{
		"package.json": `{
  "name": "checkout-svc",
  "version": "0.1.0",
  "private": true,
  "main": "dist/index.js"
}
`,
		"tsconfig.json": `{
  "compilerOptions": { "target": "ES2022", "module": "commonjs", "outDir": "dist", "rootDir": "src", "strict": true }
}
`,
		"src/checkout/validator.ts": `export type Cart = { id: number; items: Array<{ sku: string; quantity: number }> };

export class CheckoutValidator {
  validate(cart: Cart): void {
    if (!cart) throw new Error("cart is required");
    if (!Array.isArray(cart.items)) throw new Error("items must be an array");
    for (const item of cart.items) {
      if (item.quantity <= 0) throw new Error("quantity must be positive");
    }
  }
}
`,
		"src/index.ts": `import { CheckoutValidator } from "./checkout/validator";
const v = new CheckoutValidator();
export { v };
`,
	}
	for relPath, content := range files {
		if err := runner.WriteFile(filepath.Join(workDir, relPath), content); err != nil {
			return err
		}
	}
	if err := runner.GitInit(workDir); err != nil {
		return err
	}
	if err := runner.GitCommitAll(workDir, "initial ts checkout fixture"); err != nil {
		return err
	}
	if err := runner.WriteFile(filepath.Join(workDir, "demo.checkout.gh"), overlayGH); err != nil {
		return err
	}
	return runner.WriteFile(filepath.Join(workDir, "demo.diff"), diff)
}

// PythonModuleWithCheckoutValidator creates a FastAPI service with a
// CheckoutValidator class whose `validate` method is flow-scoped. Used by
// phase1 specs.
func PythonModuleWithCheckoutValidator(_, workDir string, _ map[string]any) error {
	overlayGH := `selector CheckoutValidator {
  unique
  anchor qualified_name "checkout.validator.CheckoutValidator.validate"
  anchor symbol_fingerprint "f:validate/sig=Cart:None"
}

flow CheckoutValidation {
  description "Pre-payment cart validation (Python)"
  scope CheckoutValidator
  step ValidateCart targets selector { qualified_name "checkout.validator.CheckoutValidator.validate" }
}
`
	diff := `--- a/checkout/validator.py
+++ b/checkout/validator.py
@@ -23,11 +23,13 @@ class CheckoutValidator:
     """Runs cart-level invariant checks before payment is authorized."""

     def validate(self, cart: Cart) -> None:
-        """Validate a cart. Raises ValueError if any invariant fails."""
+        """Validate a cart. Raises ValueError if any invariant fails. (unreviewed edit)"""
+        # unreviewed edit: belt-and-suspenders sanity check
         if cart is None:
             raise ValueError("cart is required")
         if not isinstance(cart.items, list):
             raise ValueError("items must be a list")
         for item in cart.items:
             if item.quantity <= 0:
                 raise ValueError("quantity must be positive")
+        return None
`
	files := map[string]string{
		"pyproject.toml":       "[project]\nname = \"checkout-svc\"\nversion = \"0.1.0\"\nrequires-python = \">=3.11\"\n",
		"checkout/__init__.py": "from .validator import Cart, CartItem, CheckoutValidator\n\n__all__ = [\"Cart\", \"CartItem\", \"CheckoutValidator\"]\n",
		"checkout/validator.py": `from dataclasses import dataclass
from typing import List


@dataclass
class CartItem:
    sku: str
    quantity: int


@dataclass
class Cart:
    id: int
    items: List[CartItem]


class CheckoutValidator:
    def validate(self, cart: Cart) -> None:
        if cart is None:
            raise ValueError("cart is required")
        if not isinstance(cart.items, list):
            raise ValueError("items must be a list")
        for item in cart.items:
            if item.quantity <= 0:
                raise ValueError("quantity must be positive")
`,
	}
	for relPath, content := range files {
		if err := runner.WriteFile(filepath.Join(workDir, relPath), content); err != nil {
			return err
		}
	}
	if err := runner.GitInit(workDir); err != nil {
		return err
	}
	if err := runner.GitCommitAll(workDir, "initial python checkout fixture"); err != nil {
		return err
	}
	if err := runner.WriteFile(filepath.Join(workDir, "demo.checkout.gh"), overlayGH); err != nil {
		return err
	}
	return runner.WriteFile(filepath.Join(workDir, "demo.diff"), diff)
}

// PolyglotRepoGoTSPy stitches a single repository where the same
// CheckoutValidator concept lives in Go (Validate), TypeScript (validate),
// and Python (validate) under their idiomatic project layouts. The fixture
// is the three-source unification check: with all three extractors active
// the unifier emits one logical entity per language with merged provenance.
func PolyglotRepoGoTSPy(_, workDir string, _ map[string]any) error {
	files := map[string]string{
		"go.mod": "module example.com/polyglot\n\ngo 1.23\n",
		"services/payments/checkout/validator.go": `package checkout

type CheckoutValidator struct{}

func (v *CheckoutValidator) Validate(cart any) error { return nil }
`,
		"web/package.json": `{ "name": "polyglot-web", "version": "0.1.0", "private": true }
`,
		"web/tsconfig.json": `{ "compilerOptions": { "target": "ES2022", "module": "commonjs", "outDir": "dist", "rootDir": "src", "strict": true } }
`,
		"web/src/checkout/validator.ts": `export class CheckoutValidator {
  validate(cart: { id: number }): void {
    if (!cart) throw new Error("cart is required");
  }
}
`,
		"audit/pyproject.toml":       "[project]\nname = \"audit\"\nversion = \"0.1.0\"\n",
		"audit/checkout/__init__.py": "from .validator import CheckoutValidator\n",
		"audit/checkout/validator.py": `class CheckoutValidator:
    def validate(self, cart) -> None:
        if cart is None:
            raise ValueError("cart is required")
`,
	}
	overlayGH := `selector CheckoutValidator {
  anchor qualified_name "CheckoutValidator.validate"
  anchor symbol_fingerprint "f:validate"
  anchor path_glob "**/checkout/validator.{go,ts,py}"
}

flow CheckoutValidation {
  description "Pre-payment cart validation across Go / TypeScript / Python"
  scope CheckoutValidator
  step ValidateCart targets CheckoutValidator
}
`
	for relPath, content := range files {
		if err := runner.WriteFile(filepath.Join(workDir, relPath), content); err != nil {
			return err
		}
	}
	if err := runner.GitInit(workDir); err != nil {
		return err
	}
	if err := runner.GitCommitAll(workDir, "initial polyglot fixture"); err != nil {
		return err
	}
	return runner.WriteFile(filepath.Join(workDir, "demo.checkout.gh"), overlayGH)
}

// PolyglotCollidingUserSave creates a small repository with `User.save`
// declared in both Go and Python under identical qualified names. The
// fixture exercises SPEC §6.12's `language_id` discriminator: the
// canonical content-addressable IDs must be distinct because the
// FunctionID hash mixes the language_id prefix, so two materialized
// entities — one per language — coexist with different IDs.
//
// Both files are placed so the tree-sitter parsers emit the same
// dotted qualified name "User.save":
//   - Go: package `User`, top-level `func save` → "User.save"
//   - Python: module `User.py` at workspace root, top-level
//     `def save` → "User.save" (pyModuleName = file basename
//     without `.py`, joined with the function name)
//
// A class wrapper or nested directory layout in the Python file
// would prefix the qualified name (e.g. "users.User.User.save")
// and break the collision premise — this fixture deliberately keeps
// the structure flat to match the Go shape byte-for-byte.
func PolyglotCollidingUserSave(_, workDir string, _ map[string]any) error {
	files := map[string]string{
		"go.mod": "module example.com/users\n\ngo 1.23\n",
		// Go: package `User`, function `save` at file scope. The qualified
		// name surfaced by tree-sitter is "User.save".
		"User/save.go": `package User

// save persists the user record. Same qualified name ("User.save") as the
// Python sibling — language_id is the only thing that disambiguates them.
func save(id int) error { return nil }
`,
		// Python: top-level `save` function in `User.py` at the
		// workspace root. pyModuleName("User.py") → "User", combined
		// with the top-level function name produces "User.save".
		"User.py": `def save(user_id: int) -> None:
    """Persist the user record. Same qualified name as the Go sibling."""
    return None
`,
	}
	for relPath, content := range files {
		if err := runner.WriteFile(filepath.Join(workDir, relPath), content); err != nil {
			return err
		}
	}
	if err := runner.GitInit(workDir); err != nil {
		return err
	}
	return runner.GitCommitAll(workDir, "initial colliding-name polyglot fixture")
}
