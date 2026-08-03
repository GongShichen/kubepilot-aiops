package service

import "sync"

type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[chan []byte]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[string]map[chan []byte]struct{}{}} }
func (h *Hub) Subscribe(id string) (chan []byte, func()) {
	ch := make(chan []byte, 32)
	h.mu.Lock()
	if h.subs[id] == nil {
		h.subs[id] = map[chan []byte]struct{}{}
	}
	h.subs[id][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() { h.mu.Lock(); delete(h.subs[id], ch); close(ch); h.mu.Unlock() }
}
func (h *Hub) Publish(id string, b []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs[id] {
		select {
		case ch <- append([]byte(nil), b...):
		default:
		}
	}
}
