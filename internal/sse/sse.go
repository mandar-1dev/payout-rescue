// Package sse is a tiny server-sent-events hub.
package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type Hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func New() *Hub { return &Hub{subs: map[chan []byte]struct{}{}} }

// Publish sends an event to every subscriber; slow subscribers drop events.
func (h *Hub) Publish(event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	msg := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, b))
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (h *Hub) subscribe() (chan []byte, func()) {
	ch := make(chan []byte, 32)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() { h.mu.Lock(); delete(h.subs, ch); h.mu.Unlock() }
}

// ServeHTTP streams events until the client disconnects.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, cancel := h.subscribe()
	defer cancel()
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	t := time.NewTicker(25 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m := <-ch:
			w.Write(m)
			fl.Flush()
		case <-t.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}
