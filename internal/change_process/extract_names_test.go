package change_process

import (
	"reflect"
	"testing"
)

func TestExtractFunctionNames_Go(t *testing.T) {
	// parseUnifiedDiff strips the leading `+` before passing lines here.
	added := []string{
		"func Validate(cart any) error {",
		"func (v *CheckoutValidator) Validate(cart any) error {",
		"	return nil",
		"func notExported(_ int) {", // unexported, intentionally skipped
		"func (s *Service) HandleOrder(o Order) (Order, error) {",
	}
	got := extractFunctionNames("internal/checkout/validator.go", added)
	want := []string{"Validate", "Validate", "HandleOrder"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestExtractFunctionNames_TypeScript(t *testing.T) {
	added := []string{
		"export function validateCart(cart: Cart): void {",
		"async function fetchOrder(id: string): Promise<Order> {",
		"export const handleCheckout = async (req: Request) => {",
		"const noop = () => {};",
		"  validate(input: CheckoutInput): CheckoutResult {", // class method
		"  if (cart.empty) {",                                // reserved word leading line — not a method
		"  for (const item of cart.items) {",                 // reserved word leading line — not a method
		"  static fromJSON(raw: any): Checkout {",            // static method
		"function* counter(): Generator<number> {",           // generator
	}
	got := extractFunctionNames("src/checkout/validator.ts", added)
	want := []string{"validateCart", "fetchOrder", "handleCheckout", "noop", "validate", "fromJSON", "counter"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestExtractFunctionNames_TypeScript_TSX(t *testing.T) {
	added := []string{
		"export const Component = ({ cart }: Props) => {",
	}
	got := extractFunctionNames("src/ui/Component.tsx", added)
	want := []string{"Component"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tsx: got %#v, want %#v", got, want)
	}
}

func TestExtractFunctionNames_Python(t *testing.T) {
	added := []string{
		"def validate_cart(cart):",
		"async def fetch_order(order_id: str) -> Order:",
		"    return None",
		"    def _internal_helper(self, x):", // class method, indented
		"    async def stream_orders(self):", // async method, indented
		"# def commented_out():",             // comment line — should not match
	}
	got := extractFunctionNames("services/checkout/validator.py", added)
	want := []string{"validate_cart", "fetch_order", "_internal_helper", "stream_orders"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestExtractFunctionNames_UnknownExtensionReturnsNil(t *testing.T) {
	if got := extractFunctionNames("README.md", []string{"# Title"}); got != nil {
		t.Errorf("unknown ext should return nil; got %#v", got)
	}
	if got := extractFunctionNames("docs/spec.txt", []string{"text"}); got != nil {
		t.Errorf("unknown ext should return nil; got %#v", got)
	}
}

func TestDetectLanguageFromPath(t *testing.T) {
	cases := map[string]string{
		"foo.go":              "go",
		"foo.ts":              "ts",
		"foo.tsx":             "ts",
		"foo.js":              "ts",
		"foo.jsx":             "ts",
		"foo.mjs":             "ts",
		"foo.cjs":             "ts",
		"foo.py":              "py",
		"a/b/c.go":            "go",
		"src/server/index.ts": "ts",
		"app/main.py":         "py",
		"README.md":           "",
		"":                    "",
	}
	for path, want := range cases {
		if got := detectLanguageFromPath(path); got != want {
			t.Errorf("detectLanguageFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}
