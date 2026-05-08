"""FastAPI dependency-injection auth: every checkout request runs through
``require_valid_cart`` which invokes the flow-scoped CheckoutValidator.
"""

from fastapi import Depends, HTTPException

from checkout import Cart, CheckoutValidator


_validator = CheckoutValidator()


def require_valid_cart(cart: Cart) -> Cart:
    try:
        _validator.validate(cart)
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc
    return cart


def auth_dependency() -> CheckoutValidator:
    """DI hook the API layer wires onto the /checkout route."""
    return _validator
