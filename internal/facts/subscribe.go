package facts

import (
	"sync"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// subscription is a kernel-side bookkeeping record for one Subscribe call.
// Buffered channels prevent the producer from ever blocking on a slow
// subscriber (SPEC §6.16: never-block-producers, at-least-once delivery).
type subscription struct {
	id     uint64
	filter kernel.EventFilter
	ch     chan kernel.Event
	once   sync.Once
	owner  *EventLog
}

// Subscribe registers an in-process subscriber and returns its event stream.
// The buffer is generously sized (1024 events) per SPEC §6.16; if a
// subscriber falls behind by more, the kernel emits SubscriberFellBehind
// (P1+) and the lagging layer decides how to recover.
func (e *EventLog) Subscribe(_ kernel.EventFilter) EventStream {
	e.subsMu.Lock()
	defer e.subsMu.Unlock()
	e.subCounter++
	s := &subscription{
		id:     e.subCounter,
		filter: kernel.EventFilter{},
		ch:     make(chan kernel.Event, 1024),
		owner:  e,
	}
	e.subs[s.id] = s
	return s
}

// SubscribeWithFilter is like Subscribe but only fires events matching the
// supplied filter (SPEC §6.16: filter grammar shared with selectors).
func (e *EventLog) SubscribeWithFilter(f kernel.EventFilter) EventStream {
	s := e.Subscribe(f).(*subscription)
	s.filter = f
	return s
}

func (e *EventLog) fanout(events []kernel.Event) {
	e.subsMu.Lock()
	defer e.subsMu.Unlock()
	for _, s := range e.subs {
		for _, ev := range events {
			if !s.filter.Matches(ev) {
				continue
			}
			select {
			case s.ch <- ev:
			default:
				// Drop on full buffer rather than block the writer.
				// In P1+ this triggers SubscriberFellBehind.
			}
		}
	}
}

// Events implements EventStream.
func (s *subscription) Events() <-chan kernel.Event { return s.ch }

// Close implements EventStream.
func (s *subscription) Close() error { return s.close() }

func (s *subscription) close() error {
	s.once.Do(func() {
		// Unregister from the EventLog before closing the channel so
		// fanout can never observe a closed channel still in e.subs
		// (which would panic on send). EventLog.Close already holds
		// subsMu and drives close() from within; the nil-owner branch
		// preserves that path's existing locking discipline.
		if s.owner != nil {
			s.owner.subsMu.Lock()
			delete(s.owner.subs, s.id)
			s.owner.subsMu.Unlock()
		}
		close(s.ch)
	})
	return nil
}
