package agent

import (
	"sync"
	"time"
)

// limitedRead runs fn at most once at a time and never makes a caller wait
// longer than wait. A second caller receives the last finished value at once,
// so a collector that ignores its deadline cannot pile up or stall the next sample.
type limitedRead[T any] struct {
	mu   sync.Mutex
	busy bool
	ok   bool
	val  T
	done chan struct{}
}

func (s *limitedRead[T]) get(wait time.Duration, fn func() T) (T, bool) {
	s.mu.Lock()
	if s.busy {
		ch, ok, val := s.done, s.ok, s.val
		s.mu.Unlock()
		if ok {
			return val, true
		}
		if wait <= 0 {
			var zero T
			return zero, false
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ch:
			s.mu.Lock()
			val = s.val
			s.mu.Unlock()
			return val, true
		case <-timer.C:
			var zero T
			return zero, false
		}
	}
	s.busy = true
	s.done = make(chan struct{})
	ch := s.done
	s.mu.Unlock()

	finished := make(chan T, 1)
	go func() {
		v := fn()
		s.mu.Lock()
		s.val = v
		s.ok = true
		s.busy = false
		close(ch)
		s.mu.Unlock()
		finished <- v
	}()

	if wait <= 0 {
		s.mu.Lock()
		val, ok := s.val, s.ok
		s.mu.Unlock()
		return val, ok
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case v := <-finished:
		return v, true
	case <-timer.C:
		s.mu.Lock()
		val, ok := s.val, s.ok
		s.mu.Unlock()
		return val, ok
	}
}
