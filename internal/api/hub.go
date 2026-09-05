package api

import (
	"sync"
	"sync/atomic"
)

// EventType names an SSE event. These are the wire contract for the UI's live
// updates and for its query invalidation.
type EventType string

const (
	// EventDeviceStatus fires on a state transition, not on every probe.
	EventDeviceStatus EventType = "device_status"
	// EventGroupStatus carries a group's tallies after a member changed state.
	EventGroupStatus EventType = "group_status"
	// EventHeartbeat fires on every probe result: this is what keeps a row's
	// status strip moving, and the only high-rate event, which is why a
	// subscriber can filter it out.
	EventHeartbeat EventType = "heartbeat"
	// EventIncident fires when an outage opens or closes.
	EventIncident EventType = "incident"
	// EventSettings fires after a settings write, so every open window
	// re-reads rather than showing a stale form.
	EventSettings EventType = "settings"
)

// AllEventTypes is the default subscription.
var AllEventTypes = []EventType{
	EventDeviceStatus, EventGroupStatus, EventHeartbeat, EventIncident, EventSettings,
}

// Event is one message on the bus. Data must be JSON-encodable.
type Event struct {
	Type EventType
	Data any
}

// Hub fans events out to connected SSE clients.
//
// Publish never blocks and never fails. The engine's evaluator publishes from
// the single goroutine that owns every state transition, so a stalled browser
// tab must not be able to slow down monitoring — a subscriber that cannot keep
// up loses events instead.
type Hub struct {
	mu      sync.Mutex
	subs    map[int64]*subscriber
	nextID  int64
	dropped atomic.Int64
}

type subscriber struct {
	ch    chan Event
	types map[EventType]bool
}

// NewHub returns an empty hub. It costs nothing until something subscribes.
func NewHub() *Hub {
	return &Hub{subs: map[int64]*subscriber{}}
}

// subscriberBuffer is how far behind a client may fall before it starts losing
// events. At 200 devices on a 30 s interval the heartbeat feed is about seven
// events a second, so this is roughly thirty seconds of slack.
const subscriberBuffer = 256

// Subscribe registers a listener for the given event types, all of them when
// types is empty. The returned function unsubscribes and must be called.
func (h *Hub) Subscribe(types ...EventType) (<-chan Event, func()) {
	if len(types) == 0 {
		types = AllEventTypes
	}
	want := make(map[EventType]bool, len(types))
	for _, t := range types {
		want[t] = true
	}

	sub := &subscriber{ch: make(chan Event, subscriberBuffer), types: want}

	h.mu.Lock()
	h.nextID++
	id := h.nextID
	h.subs[id] = sub
	h.mu.Unlock()

	var once sync.Once
	return sub.ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, id)
			h.mu.Unlock()
			close(sub.ch)
		})
	}
}

// Publish delivers ev to every interested subscriber, dropping it for any
// whose buffer is full.
//
// A nil Hub publishes nothing, so the engine can run without an API — which is
// exactly what the engine tests do.
func (h *Hub) Publish(ev Event) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, sub := range h.subs {
		if !sub.types[ev.Type] {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			h.dropped.Add(1)
		}
	}
}

// Subscribers reports how many streams are connected.
func (h *Hub) Subscribers() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Dropped reports how many events have been discarded because a subscriber was
// too far behind. Exposed on /api/health so a UI that is silently missing
// updates is diagnosable.
func (h *Hub) Dropped() int64 {
	if h == nil {
		return 0
	}
	return h.dropped.Load()
}
