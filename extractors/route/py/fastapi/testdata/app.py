"""FastAPI fixture for the routes.py.fastapi extractor tests."""

from fastapi import APIRouter, FastAPI
from pydantic import BaseModel

app = FastAPI()
router = APIRouter(prefix="/api/v1")


class OrderOut(BaseModel):
    id: int
    name: str


@app.get("/health")
def health():
    """Top-level GET on the FastAPI instance."""
    return {"ok": True}


@app.post("/login")
def login():
    return {"token": "xxx"}


@router.get("/orders", response_model=OrderOut)
def list_orders():
    """Routed via APIRouter(prefix='/api/v1'); expected full path
    is /api/v1/orders."""
    return []


@router.put("/orders/{order_id}")
def update_order(order_id: int):
    return {"id": order_id}


@router.delete("/orders/{order_id}")
def delete_order(order_id: int):
    return None


@router.patch("/orders/{order_id}")
def patch_order(order_id: int):
    return None
