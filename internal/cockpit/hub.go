package cockpit

import (
	"encoding/json"
	"sync"
)

// Hub buffers recent observations and fans them out to SSE subscribers with seq-based
// resume (EDITOR_PLUGIN_PLAN.md §3.2). The editor's framed events (via the cockpit
// Client) are Publish()ed here; each browser GET /events subscription drains the
// backlog after its Last-Event-ID, then streams live — so a dropped SSE connection
// resumes without a gap as long as the requested seq is still in the ring.
type Hub struct {
	mu       sync.Mutex
	ring     []HubEvent
	capacity int
	head     int // ring is full once len==capacity; head is the oldest slot
	full     bool
	subs     map[int]*subscriber
	nextSub  int
	lastSeq  uint64
}

// HubEvent is one buffered observation. Seq is the editor's monotonic event seq.
type HubEvent struct {
	Seq  uint64          `json:"seq"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type subscriber struct {
	ch     chan HubEvent
	closed bool
}

// NewHub returns a Hub buffering the last `capacity` events for resume.
func NewHub(capacity int) *Hub {
	if capacity < 1 {
		capacity = 1
	}
	return &Hub{capacity: capacity, subs: map[int]*subscriber{}}
}

// Publish records an event and fans it out to live subscribers. A slow subscriber that
// can't keep up is dropped-oldest at its own channel (never blocks Publish), matching
// the editor-side drop-oldest discipline — the browser detects the gap via seq and can
// reconnect with Last-Event-ID to replay from the ring.
func (h *Hub) Publish(seq uint64, etype string, data json.RawMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ev := HubEvent{Seq: seq, Type: etype, Data: data}
	if seq > h.lastSeq {
		h.lastSeq = seq
	}
	// append to the ring
	if !h.full && len(h.ring) < h.capacity {
		h.ring = append(h.ring, ev)
		if len(h.ring) == h.capacity {
			h.full = true
			h.head = 0
		}
	} else {
		h.ring[h.head] = ev
		h.head = (h.head + 1) % h.capacity
	}
	for _, s := range h.subs {
		select {
		case s.ch <- ev:
		default:
			// Genuine drop-OLDEST: a live control surface must show the freshest state,
			// so discard the oldest buffered event to make room for this one. The browser
			// sees the seq gap and can reconnect with Last-Event-ID to replay from the ring.
			select {
			case <-s.ch:
			default:
			}
			select {
			case s.ch <- ev:
			default:
			}
		}
	}
}

// backlogLocked returns buffered events with Seq > fromSeq, in seq order.
func (h *Hub) backlogLocked(fromSeq uint64) []HubEvent {
	var out []HubEvent
	n := len(h.ring)
	for i := 0; i < n; i++ {
		idx := i
		if h.full {
			idx = (h.head + i) % h.capacity // iterate oldest→newest
		}
		if h.ring[idx].Seq > fromSeq {
			out = append(out, h.ring[idx])
		}
	}
	return out
}

// Subscribe registers a live subscriber and returns its id, channel, and the backlog of
// buffered events after fromSeq (to deliver before streaming live). Caller must
// Unsubscribe when done.
func (h *Hub) Subscribe(fromSeq uint64, buffer int) (int, <-chan HubEvent, []HubEvent) {
	if buffer < 1 {
		buffer = 256
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.nextSub
	h.nextSub++
	s := &subscriber{ch: make(chan HubEvent, buffer)}
	h.subs[id] = s
	return id, s.ch, h.backlogLocked(fromSeq)
}

// Unsubscribe removes a subscriber and closes its channel exactly once.
func (h *Hub) Unsubscribe(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.subs[id]; ok {
		delete(h.subs, id)
		if !s.closed {
			s.closed = true
			close(s.ch)
		}
	}
}

// LastSeq is the highest published seq (the resume high-water mark).
func (h *Hub) LastSeq() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastSeq
}

// SubscriberCount is exposed for tests/observability.
func (h *Hub) SubscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
