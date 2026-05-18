package jsonrpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sourcegraph/jsonrpc2"
)

// IdentifyParams is the param shape for kernel.identify.
type IdentifyParams struct {
	SubscriberID string `json:"subscriber_id"`
}

// IdentifyResult echoes the bound subscriber_id back to the client.
type IdentifyResult struct {
	SubscriberID string `json:"subscriber_id"`
}

// Identify implements kernel.identify (SPEC §6.22 connect step).
func (s *Service) Identify(ctx context.Context, conn *jsonrpc2.Conn, p IdentifyParams) (IdentifyResult, error) {
	s.subscriptions().Identify(conn, p.SubscriberID)
	return IdentifyResult{SubscriberID: s.subscriptions().SubscriberIDFor(conn)}, nil
}

// SubscribeParams is the param shape for kernel.subscribe.
type SubscribeParams struct {
	// Filter is a layer or layer/kind shorthand selecting which
	// events fan out to this subscription. Empty matches everything.
	Filter string `json:"filter"`
	// Cursor is the last seq the client already processed. When set,
	// the daemon resumes from cursor+1; when zero, the stream begins
	// at the current head.
	Cursor uint64 `json:"cursor,omitempty"`
	// QueueCap overrides the default per-subscription backpressure
	// window. Backpressure here is measured as (cursor - ackedSeq):
	// when the gap exceeds QueueCap, the daemon emits one
	// `kernel.fellBehind` notification carrying the client's
	// recovery options. Zero/omitted uses the default (SPEC §6.22).
	QueueCap int `json:"queue_cap,omitempty"`
}

// SubscribeResult carries the subscription_id back to the client.
type SubscribeResult struct {
	SubscriptionID string `json:"subscription_id"`
	SubscriberID   string `json:"subscriber_id"`
}

// Subscribe implements kernel.subscribe (SPEC §6.22 subscribe step).
func (s *Service) Subscribe(ctx context.Context, conn *jsonrpc2.Conn, p SubscribeParams) (SubscribeResult, error) {
	id, err := s.subscriptions().SubscribeWithOpts(ctx, conn, p.Filter, p.Cursor, p.QueueCap)
	if err != nil {
		return SubscribeResult{}, err
	}
	return SubscribeResult{
		SubscriptionID: id,
		SubscriberID:   s.subscriptions().SubscriberIDFor(conn),
	}, nil
}

// AckParams is the param shape for kernel.ack.
type AckParams struct {
	SubscriptionID string `json:"subscription_id"`
	Seq            uint64 `json:"seq"`
}

// Ack implements kernel.ack (SPEC §6.22 ack step).
func (s *Service) Ack(ctx context.Context, p AckParams) (struct{}, error) {
	if err := s.subscriptions().Ack(p.SubscriptionID, p.Seq); err != nil {
		return struct{}{}, err
	}
	return struct{}{}, nil
}

// CancelParams is the param shape for kernel.cancel.
type CancelParams struct {
	// RequestID is the client's JSON-RPC request id of an in-flight
	// call this client wants to abort (SPEC §6.23). Accepts either
	// numeric or string ids — the conn-side dispatcher uses the
	// same normalization as the in-flight registry.
	RequestID json.RawMessage `json:"request_id"`
}

// CancelResult reports whether a matching in-flight request was
// found + cancelled. Cancelling an already-finished or unknown
// request is not an error — it just reports cancelled=false.
type CancelResult struct {
	Cancelled bool `json:"cancelled"`
}

// UnsubscribeParams is the param shape for kernel.unsubscribe.
type UnsubscribeParams struct {
	SubscriptionID string `json:"subscription_id"`
}

// Unsubscribe implements kernel.unsubscribe.
func (s *Service) Unsubscribe(ctx context.Context, p UnsubscribeParams) (struct{}, error) {
	if p.SubscriptionID == "" {
		return struct{}{}, fmt.Errorf("unsubscribe: subscription_id required")
	}
	s.subscriptions().Unsubscribe(p.SubscriptionID)
	return struct{}{}, nil
}

// PingResult mirrors daemon.ping but is specific to the kernel
// namespace so monitoring callers can distinguish RPC liveness from
// subscriber liveness. SPEC §6.22 heartbeat clause.
type KernelPingResult struct {
	Pong bool `json:"pong"`
}

// KernelPing implements kernel.ping (SPEC §6.22 heartbeat).
// Returning {pong: true} is the wire signal that the subscriber's
// connection is alive — callers that issue ping on an idle timer
// can use the round-trip as a liveness probe and trigger
// reconnection on failure. The daemon-side eviction path uses the
// last-seen time of every conn (touched on every RPC) to evict
// silent subscribers after the configured threshold; eviction
// emits SubscriberEvicted on the kernel bus.
func (s *Service) KernelPing(_ context.Context) (KernelPingResult, error) {
	return KernelPingResult{Pong: true}, nil
}

// registerConn binds method to a connection-aware handler. Mirrors
// Register but threads the conn into the handler so notification
// pushes know which client to address.
func registerConn[P any, R any](s *Server, method, capName string, fn func(ctx context.Context, conn *jsonrpc2.Conn, p P) (R, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.methods[method] = methodEntry{
		cap: capName,
		connHandler: func(ctx context.Context, conn *jsonrpc2.Conn, raw json.RawMessage) (any, error) {
			var p P
			if len(raw) > 0 && string(raw) != "null" {
				if err := json.Unmarshal(raw, &p); err != nil {
					return nil, &jsonrpc2.Error{Code: -32602, Message: fmt.Sprintf("invalid params: %v", err)}
				}
			}
			return fn(ctx, conn, p)
		},
	}
}
