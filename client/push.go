package client

import (
	"encoding/json/jsontext"
	"sync"
)

// pushHandler runs one OnPush callback on its own goroutine, in arrival
// order. The read loop only appends to the queue, so a slow handler never
// holds up the connection, and a handler may make calls on the client
// without deadlocking it. Push events are never dropped, so the queue is
// unbounded.
type pushHandler struct {
	handle func(jsontext.Value)

	mu      sync.Mutex
	queue   []jsontext.Value
	running bool
	removed bool
}

func (h *pushHandler) enqueue(data jsontext.Value) {
	h.mu.Lock()
	if h.removed {
		h.mu.Unlock()
		return
	}
	h.queue = append(h.queue, data)
	if h.running {
		h.mu.Unlock()
		return
	}
	h.running = true
	h.mu.Unlock()
	go h.drain()
}

func (h *pushHandler) drain() {
	for {
		h.mu.Lock()
		if len(h.queue) == 0 || h.removed {
			h.queue = nil
			h.running = false
			h.mu.Unlock()
			return
		}
		data := h.queue[0]
		h.queue[0] = nil
		h.queue = h.queue[1:]
		h.mu.Unlock()
		h.handle(data)
	}
}

// OnPush calls fn for every push event with the given name, for example one
// sent with aprot's Server.Broadcast or Conn.Push. Each registration gets
// its events in order, on its own goroutine, so fn may block or make calls
// on the client. An event whose data does not decode into T is logged and
// skipped.
//
// The returned function removes the handler. Events already queued for it
// are discarded.
func OnPush[T any](c *Client, event string, fn func(T)) (remove func()) {
	h := &pushHandler{}
	h.handle = func(data jsontext.Value) {
		var v T
		if len(data) > 0 {
			if err := unmarshalWire(data, &v); err != nil {
				c.logger.Warn("aprot client: undecodable push event", "event", event, "err", err)
				return
			}
		}
		fn(v)
	}
	c.mu.Lock()
	if c.pushes[event] == nil {
		c.pushes[event] = make(map[*pushHandler]struct{})
	}
	c.pushes[event][h] = struct{}{}
	c.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.pushes[event], h)
			if len(c.pushes[event]) == 0 {
				delete(c.pushes, event)
			}
			c.mu.Unlock()
			h.mu.Lock()
			h.removed = true
			h.queue = nil
			h.mu.Unlock()
		})
	}
}

func (c *Client) dispatchPush(event string, data jsontext.Value) {
	c.mu.Lock()
	handlers := make([]*pushHandler, 0, len(c.pushes[event]))
	for h := range c.pushes[event] {
		handlers = append(handlers, h)
	}
	c.mu.Unlock()
	for _, h := range handlers {
		h.enqueue(data)
	}
}
