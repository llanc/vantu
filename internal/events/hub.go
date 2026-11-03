package events

import (
	"context"
	"sync"
	"time"
)

// Event represents a server-sent event pushed to Web UI or CLI watchers.
type Event struct {
	Type      string    `json:"type"`
	Data      any       `json:"data"`
	Timestamp time.Time `json:"timestamp"`
}

// Hub is a lightweight, non-blocking pub/sub event broker for SSE clients.
type Hub struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

func NewHub() *Hub {
	return &Hub{
		subs: make(map[chan Event]struct{}),
	}
}

// Subscribe registers a new event channel that automatically closes when ctx is done.
func (h *Hub) Subscribe(ctx context.Context) <-chan Event {
	ch := make(chan Event, 32)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	go func() {
		<-ctx.Done()
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}()

	return ch
}

// Publish broadcasts an event to all active subscribers without blocking if a client is slow.
func (h *Hub) Publish(eventType string, data any) {
	if h == nil {
		return
	}
	ev := Event{
		Type:      eventType,
		Data:      data,
		Timestamp: time.Now().UTC(),
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
			// Drop oldest event if subscriber buffer is full so producer never blocks.
		}
	}
}
