package acp

import (
	"sync"
	"testing"
	"time"
)

func TestUnboundedQueuePushPop(t *testing.T) {
	q := newUnboundedQueue[int]()

	q.push(1)
	q.push(2)
	q.push(3)

	v, ok := q.pop()
	if !ok || v != 1 {
		t.Errorf("expected (1, true), got (%d, %v)", v, ok)
	}

	v, ok = q.pop()
	if !ok || v != 2 {
		t.Errorf("expected (2, true), got (%d, %v)", v, ok)
	}

	v, ok = q.pop()
	if !ok || v != 3 {
		t.Errorf("expected (3, true), got (%d, %v)", v, ok)
	}
}

func TestUnboundedQueuePopBlocksUntilPush(t *testing.T) {
	q := newUnboundedQueue[string]()

	done := make(chan string)
	go func() {
		v, ok := q.pop()
		if ok {
			done <- v
		}
	}()

	// Give the goroutine time to block
	time.Sleep(10 * time.Millisecond)

	select {
	case <-done:
		t.Fatal("pop should have blocked")
	default:
	}

	q.push("hello")

	select {
	case v := <-done:
		if v != "hello" {
			t.Errorf("expected 'hello', got %q", v)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("pop did not unblock after push")
	}
}

func TestUnboundedQueueCloseUnblocksPop(t *testing.T) {
	q := newUnboundedQueue[int]()

	done := make(chan bool)
	go func() {
		_, ok := q.pop()
		done <- ok
	}()

	// Give the goroutine time to block
	time.Sleep(10 * time.Millisecond)

	q.close()

	select {
	case ok := <-done:
		if ok {
			t.Error("expected ok=false after close on empty queue")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("pop did not unblock after close")
	}
}

func TestUnboundedQueueDrainAfterClose(t *testing.T) {
	q := newUnboundedQueue[int]()

	q.push(1)
	q.push(2)
	q.close()

	// Should still be able to drain existing items
	v, ok := q.pop()
	if !ok || v != 1 {
		t.Errorf("expected (1, true), got (%d, %v)", v, ok)
	}

	v, ok = q.pop()
	if !ok || v != 2 {
		t.Errorf("expected (2, true), got (%d, %v)", v, ok)
	}

	// Now should return false
	_, ok = q.pop()
	if ok {
		t.Error("expected ok=false after draining closed queue")
	}
}

func TestUnboundedQueueLen(t *testing.T) {
	q := newUnboundedQueue[int]()

	if q.len() != 0 {
		t.Errorf("expected len 0, got %d", q.len())
	}

	q.push(1)
	q.push(2)

	if q.len() != 2 {
		t.Errorf("expected len 2, got %d", q.len())
	}

	q.pop()

	if q.len() != 1 {
		t.Errorf("expected len 1, got %d", q.len())
	}
}

func TestUnboundedQueueConcurrentPushPop(t *testing.T) {
	q := newUnboundedQueue[int]()
	const n = 1000

	var wg sync.WaitGroup

	// Producer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			q.push(i)
		}
		q.close()
	}()

	// Consumer - verify ordering
	received := make([]int, 0, n)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			v, ok := q.pop()
			if !ok {
				break
			}
			received = append(received, v)
		}
	}()

	wg.Wait()

	if len(received) != n {
		t.Fatalf("expected %d items, got %d", n, len(received))
	}

	for i, v := range received {
		if v != i {
			t.Errorf("ordering broken: expected %d at index %d, got %d", i, i, v)
			break
		}
	}
}

func TestUnboundedQueueMultipleProducers(t *testing.T) {
	q := newUnboundedQueue[int]()
	const producers = 10
	const itemsPerProducer = 100

	var wg sync.WaitGroup

	// Multiple producers
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < itemsPerProducer; i++ {
				q.push(i)
			}
		}()
	}

	// Wait for all producers, then close
	go func() {
		wg.Wait()
		q.close()
	}()

	// Consumer
	count := 0
	for {
		_, ok := q.pop()
		if !ok {
			break
		}
		count++
	}

	expected := producers * itemsPerProducer
	if count != expected {
		t.Errorf("expected %d items, got %d", expected, count)
	}
}
