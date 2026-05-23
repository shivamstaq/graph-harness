"""Audit consumer for the order.created Kafka topic.

The qualified name ``consumer.handle_order_created`` is the flow's
ConsumeAudit anchor — touching this without acknowledging
``OrderCreatedPropagation`` must still pass; mutating the Go publisher
without mirroring the change here is what fires
``missing_dependent_update``.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Optional

from confluent_kafka import Consumer


@dataclass
class OrderCreatedPayload:
    OrderID: str
    CustomerID: str
    Amount: float


def handle_order_created(payload: OrderCreatedPayload) -> None:
    """Persist the order to the audit ledger.

    The body exercises every payload field so a removal in the
    producer surfaces as a missing-attribute error here as well.
    """
    if not payload.OrderID:
        raise ValueError("missing OrderID")
    if not payload.CustomerID:
        raise ValueError("missing CustomerID")
    if payload.Amount <= 0:
        raise ValueError("non-positive amount")


def run_audit_consumer() -> Optional[None]:
    consumer = Consumer(
        {
            "bootstrap.servers": "localhost:9092",
            "group.id": "audit-order-consumer",
            "auto.offset.reset": "earliest",
        }
    )
    consumer.subscribe(["order.created"])
    while True:
        msg = consumer.poll(timeout=1.0)
        if msg is None:
            continue
        if msg.error():
            continue
        raw = msg.value().decode("utf-8")
        payload = OrderCreatedPayload(**json.loads(raw))
        handle_order_created(payload)
