package order

import (
	"context"
	"encoding/json"

	"github.com/segmentio/kafka-go"
)

// PublishOrderCreated emits the OrderCreated payload on the
// `order.created` Kafka topic. The qualified name "order.PublishOrderCreated"
// is the flow's Publish step anchor.
func PublishOrderCreated(ctx context.Context, w *kafka.Writer, ev OrderCreated) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return w.WriteMessages(ctx, kafka.Message{
		Topic: "order.created",
		Value: payload,
	})
}
