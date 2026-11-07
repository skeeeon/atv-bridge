package remote

import "sync"

type callbackEntry[T any] struct {
	id int
	fn func(T)
}

// callbacks is a registry of callbacks fired when a value is updated.
type callbacks[T any] struct {
	mu      sync.Mutex
	nextID  int
	entries []callbackEntry[T]
}

// add registers fn and returns a function that removes it again.
func (c *callbacks[T]) add(fn func(T)) (remove func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	c.entries = append(c.entries, callbackEntry[T]{id: id, fn: fn})
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, e := range c.entries {
			if e.id == id {
				c.entries = append(c.entries[:i], c.entries[i+1:]...)
				return
			}
		}
	}
}

// fire invokes all registered callbacks in registration order.
func (c *callbacks[T]) fire(v T) {
	c.mu.Lock()
	entries := make([]callbackEntry[T], len(c.entries))
	copy(entries, c.entries)
	c.mu.Unlock()
	for _, e := range entries {
		e.fn(v)
	}
}
