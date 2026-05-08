// Package checkout owns the cart validation pipeline used during the
// pre-payment phase of the checkout flow. The CheckoutValidator type is
// the canonical entry point referenced by the CheckoutValidation flow.
package checkout

import "errors"

// CheckoutValidator runs cart-level invariant checks. The Validate method
// is the flow-scoped function — touching it without acknowledging the
// CheckoutValidation flow must produce a flow_unreviewed finding.
type CheckoutValidator struct{}

// Validate runs the cart validation pipeline. Returns an error if any
// invariant fails.
func (v *CheckoutValidator) Validate(cart any) error {
	if cart == nil {
		return errors.New("cart is nil")
	}
	return nil
}
