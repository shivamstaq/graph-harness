// Web order consumer: subscribes to `order.created` on Kafka and routes
// each event through the local handler. The qualified name
// `order_consumer.handleOrderCreated` is the flow's ConsumeApp anchor.

import { Kafka, EachMessagePayload } from "kafkajs";

export type OrderCreatedPayload = {
  OrderID: string;
  CustomerID: string;
  Amount: number;
};

export async function handleOrderCreated(payload: OrderCreatedPayload): Promise<void> {
  // In the real app this would update the web cart / send a confirmation.
  // For the bench fixture the body just exercises every field so a rename
  // or removal in the producer surfaces as a type error here too.
  if (!payload.OrderID) throw new Error("missing OrderID");
  if (!payload.CustomerID) throw new Error("missing CustomerID");
  if (payload.Amount <= 0) throw new Error("non-positive amount");
}

export async function runOrderConsumer(): Promise<void> {
  const kafka = new Kafka({ clientId: "web", brokers: ["localhost:9092"] });
  const consumer = kafka.consumer({ groupId: "web-order-consumer" });
  await consumer.connect();
  await consumer.subscribe({ topic: "order.created", fromBeginning: false });
  await consumer.run({
    eachMessage: async ({ message }: EachMessagePayload) => {
      const raw = message.value?.toString() ?? "{}";
      const payload = JSON.parse(raw) as OrderCreatedPayload;
      await handleOrderCreated(payload);
    },
  });
}
