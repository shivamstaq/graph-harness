"""FastAPI app entry point. The /checkout route gates on require_valid_cart
so the flow-scoped CheckoutValidator sits on the pre-payment path."""

from fastapi import Depends, FastAPI

from auth import require_valid_cart
from checkout import Cart


def build_app() -> FastAPI:
    app = FastAPI()

    @app.post("/checkout")
    def checkout(cart: Cart = Depends(require_valid_cart)) -> dict:
        return {"status": "ok", "cart_id": cart.id}

    return app


app = build_app()
