package rest

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Broker fans plan updates out to connected browsers over Server-Sent Events.
//
// SSE rather than WebSockets: the traffic is strictly one-way (the API is
// read-only, so a browser has nothing to send), EventSource reconnects on its
// own, and it needs no dependency and no protocol upgrade. A WebSocket would
// be a bidirectional channel for a unidirectional problem.
type Broker struct {
	mu        sync.RWMutex
	clients   map[chan Event]struct{}
	log       *slog.Logger
	lastEvent *Event
}

// Event is one server-sent message.
type Event struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// NewBroker builds a broker.
func NewBroker(log *slog.Logger) *Broker {
	return &Broker{clients: make(map[chan Event]struct{}), log: log}
}

// Publish delivers an event to every connected client.
func (b *Broker) Publish(e Event) {
	b.mu.Lock()
	b.lastEvent = &e
	clients := make([]chan Event, 0, len(b.clients))
	for c := range b.clients {
		clients = append(clients, c)
	}
	b.mu.Unlock()

	for _, c := range clients {
		select {
		case c <- e:
		default:
			// A client that cannot keep up is skipped rather than blocking the
			// broker. It will catch up on its next poll of the API; dropping a
			// notification is far better than stalling every other viewer.
		}
	}
}

// Clients reports how many listeners are connected.
func (b *Broker) Clients() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.clients)
}

// ServeHTTP streams events until the client disconnects.
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// nginx buffers proxied responses by default, which would hold events back
	// until the buffer filled — for a low-volume stream, indefinitely.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// Flush immediately: without this the headers sit in the server's buffer
	// until the first event, so a client connecting to a stable cluster would
	// hang instead of establishing the stream.
	flusher.Flush()

	ch := make(chan Event, 8)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	last := b.lastEvent
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.clients, ch)
		b.mu.Unlock()
		close(ch)
	}()

	// Send the current state immediately: a client that connects between
	// plans would otherwise sit blank until the next change, which on a stable
	// cluster could be a very long time.
	if last != nil {
		writeEvent(w, flusher, *last)
	}

	// Keepalives stop idle proxies from reaping a connection that is simply
	// quiet because the cluster is stable.
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			if !writeEvent(w, flusher, e) {
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeEvent(w http.ResponseWriter, flusher http.Flusher, e Event) bool {
	payload, err := json.Marshal(e.Data)
	if err != nil {
		return true
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, payload); err != nil {
		return false
	}
	flusher.Flush()
	return true
}
