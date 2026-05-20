package facts

import (
	"context"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// TestSubscription_AppendAfterCloseDoesNotPanic guards against the F21
// fanout race (see plan/phase-0-thru-1.5-closure.md): subscription.close
// closed s.ch via sync.Once but left the subscription registered with
// EventLog.subs. The next Append → fanout iterated the map and panicked
// on `send on closed channel`. The fix unregisters under subsMu before
// closing the channel so fanout can never observe a closed sub.
func TestSubscription_AppendAfterCloseDoesNotPanic(t *testing.T) {
	log := newTestEventLog(t)
	ctx := context.Background()

	stream := log.Subscribe(kernel.EventFilter{})
	// Drain any prior buffered events.
	drainStream(stream)

	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The subscription must be unregistered after Close — otherwise
	// the next Append → fanout would race a send onto the closed
	// channel.
	log.subsMu.Lock()
	if got := len(log.subs); got != 0 {
		log.subsMu.Unlock()
		t.Fatalf("len(subs) after Close = %d, want 0", got)
	}
	log.subsMu.Unlock()

	// Append must not panic. Pre-fix this is where `send on closed
	// channel` would fire.
	if _, err := log.Append(ctx, []kernel.Event{
		{Layer: "code.core", Kind: "EntityMaterialized", ProducedBy: kernel.SourceLayerInternal, Payload: []byte(`{}`)},
	}); err != nil {
		t.Fatalf("Append after Close: %v", err)
	}
}

// TestSubscription_CloseTwiceIsIdempotent confirms the sync.Once guard
// still works after the unregister path is added.
func TestSubscription_CloseTwiceIsIdempotent(t *testing.T) {
	log := newTestEventLog(t)

	stream := log.Subscribe(kernel.EventFilter{})
	if err := stream.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestEventLog_CloseClosesActiveSubscriptions exercises the
// EventLog.Close path that now snapshots subs under subsMu, clears
// the map, and drives subscription.close outside the lock to avoid a
// self-deadlock (subscription.close re-takes subsMu when the owner is
// set).
func TestEventLog_CloseClosesActiveSubscriptions(t *testing.T) {
	log := newTestEventLog(t)

	stream := log.Subscribe(kernel.EventFilter{})

	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The subscription's channel should be drained-and-closed; reading
	// from it must return the zero value with !ok rather than block.
	select {
	case _, ok := <-stream.Events():
		if ok {
			// drain any buffered events then re-check
			drainStream(stream)
			select {
			case _, ok2 := <-stream.Events():
				if ok2 {
					t.Fatal("Events() still open after Close")
				}
			default:
				t.Fatal("Events() returned a value but is not closed")
			}
		}
	default:
		t.Fatal("Events() blocked after Close; channel should be closed")
	}
}

func drainStream(s EventStream) {
	for {
		select {
		case <-s.Events():
		default:
			return
		}
	}
}
