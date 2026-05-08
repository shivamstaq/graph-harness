"""Cart validation for the pre-payment checkout flow.

The CheckoutValidator class is the flow-scoped surface — touching its
``validate`` method without acknowledging the CheckoutValidation flow must
produce a flow_unreviewed finding from change.process.
"""

from dataclasses import dataclass
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
    """Runs cart-level invariant checks before payment is authorized."""

    def validate(self, cart: Cart) -> None:
        """Validate a cart. Raises ValueError if any invariant fails."""
        if cart is None:
            raise ValueError("cart is required")
        if not isinstance(cart.items, list):
            raise ValueError("items must be a list")
        for item in cart.items:
            if item.quantity <= 0:
                raise ValueError("quantity must be positive")
