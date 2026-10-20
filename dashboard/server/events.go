package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// SSEEvent is a single server-sent event.
type SSEEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// EventBus manages SSE client connections and broadcasts.
type EventBus struct {
	mu      sync.RWMutex
	clients map[chan SSEEvent]struct{}
}

func NewEventBus() *EventBus {
	return &EventBus{
		clients: make(map[chan SSEEvent]struct{}),
	}
}

// Subscribe adds a new SSE client. Returns a channel and a cleanup function.
func (eb *EventBus) Subscribe() (chan SSEEvent, func()) {
	// Buffer was 64 — easy to fill during a busy moment (file watcher fires +
	// transcript poller + multiple agents going busy/idle in the same tick).
	// 1024 covers a several-second backlog at our typical event rate, which
	// is enough headroom that drain-on-full below rarely kicks in.
	ch := make(chan SSEEvent, 1024)
	eb.mu.Lock()
	eb.clients[ch] = struct{}{}
	eb.mu.Unlock()

	cleanup := func() {
		eb.mu.Lock()
		delete(eb.clients, ch)
		eb.mu.Unlock()
		close(ch)
	}
	return ch, cleanup
}

// Publish sends an event to all connected SSE clients.
func (eb *EventBus) Publish(eventType string, data any) {
	evt := SSEEvent{Type: eventType, Data: data}
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	for ch := range eb.clients {
		select {
		case ch <- evt:
		default:
			// Buffer full. Previously we dropped the NEW event, which meant
			// a transient slowdown could leave the client permanently stuck
			// on stale state — newer state_updates kept getting discarded
			// while older queued ones drained. Instead drop the OLDEST and
			// admit the new event: state_update events always replace, so
			// keeping the latest is strictly better than keeping the
			// oldest. The user's bug ("UI doesn't update until I shift+
			// refresh") was almost certainly this.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- evt:
			default:
			}
		}
	}
}

// ServeSSE is an HTTP handler that streams server-sent events.
func (eb *EventBus) ServeSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch, cleanup := eb.Subscribe()
	defer cleanup()

	// Send initial keepalive
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ctx := r.Context()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			// Send as a real event (was an SSE `:` comment, which the browser
			// silently consumes — useless for client-side liveness checks).
			// As an `event: ping`, the client can listen for it and force-
			// reconnect if it doesn't see one within ~30s — covering the case
			// where the TCP socket is half-dead but EventSource hasn't
			// noticed yet (sleep/wake, flaky proxies, etc.).
			fmt.Fprintf(w, "event: ping\ndata: {}\n\n")
			flusher.Flush()
		case evt, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(evt.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, data)
			flusher.Flush()
		}
	}
}
