// Package order owns the canonical OrderCreated event payload. Every
// downstream service consuming `kafka:order.created` MUST stay in sync
// with the field shape declared here.
package order

// OrderCreated is the payload published on the `order.created` Kafka
// topic. Adding, renaming, or removing fields here invalidates every
// dependent (TS web consumer, Py audit consumer, Jest contract test)
// and must trigger `missing_dependent_update` from change.process.
type OrderCreated struct {
	OrderID    string
	CustomerID string
	Amount     float64
}
