// CheckoutValidator runs cart-level invariant checks before payment is
// authorized. The validate method is the flow-scoped function — touching
// it without acknowledging the CheckoutValidation flow must produce a
// flow_unreviewed finding.

export type Cart = {
  id: number;
  items: Array<{ sku: string; quantity: number }>;
};

export class CheckoutValidator {
  /** Runs the cart validation pipeline. Throws if any invariant fails. */
  validate(cart: Cart): void {
    if (!cart) throw new Error("cart is required");
    if (!Array.isArray(cart.items)) throw new Error("items must be an array");
    for (const item of cart.items) {
      if (item.quantity <= 0) throw new Error("quantity must be positive");
    }
  }
}
