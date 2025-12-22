package acp

import "sync"

// unboundedQueue is a thread-safe FIFO queue that never blocks on push.
// This ensures the receive loop can always enqueue notifications without
// stalling, while preserving strict ordering for the consumer.
type unboundedQueue[T any] struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []T
	closed bool
}

func newUnboundedQueue[T any]() *unboundedQueue[T] {
	q := &unboundedQueue[T]{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// push appends an item to the queue. Never blocks.
func (q *unboundedQueue[T]) push(item T) {
	q.mu.Lock()
	q.items = append(q.items, item)
	q.mu.Unlock()
	q.cond.Signal()
}

// pop removes and returns the next item, blocking until one is available.
// Returns the zero value and false if the queue is closed and empty.
func (q *unboundedQueue[T]) pop() (T, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.items) == 0 {
		var zero T
		return zero, false
	}
	item := q.items[0]
	q.items = q.items[1:]
	return item, true
}

// close signals that no more items will be pushed.
// The consumer will drain remaining items before pop returns false.
func (q *unboundedQueue[T]) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// len returns the current number of items in the queue.
func (q *unboundedQueue[T]) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}
