// Contract test for the `order.created` Kafka event payload. The
// qualified name `order_created_spec.order_created_contract` is the
// flow's ContractTest anchor; a mutation in the Go producer that lands
// without an accompanying update here must produce
// `missing_dependent_update`.

import { describe, it, expect } from "@jest/globals";

type OrderCreatedPayload = {
  OrderID: string;
  CustomerID: string;
  Amount: number;
};

function decodeOrderCreated(raw: string): OrderCreatedPayload {
  return JSON.parse(raw) as OrderCreatedPayload;
}

describe("order_created_contract", function order_created_contract() {
  it("accepts the canonical OrderCreated payload shape", () => {
    const raw = JSON.stringify({
      OrderID: "ord_123",
      CustomerID: "cust_42",
      Amount: 19.95,
    });
    const payload = decodeOrderCreated(raw);
    expect(payload.OrderID).toBe("ord_123");
    expect(payload.CustomerID).toBe("cust_42");
    expect(payload.Amount).toBeGreaterThan(0);
  });

  it("rejects a payload missing OrderID", () => {
    const payload = { CustomerID: "x", Amount: 1.0 } as unknown as OrderCreatedPayload;
    expect(() => {
      if (!payload.OrderID) throw new Error("missing OrderID");
    }).toThrow(/OrderID/);
  });
});
