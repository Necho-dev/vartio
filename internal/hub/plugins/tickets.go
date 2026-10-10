package plugin

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// TicketTTL matches the hub's terminal and display tickets.
const TicketTTL = 2 * time.Minute

// Tickets are one-shot, short-lived capabilities for PublicPrefix routes:
// an authenticated API call issues one, a header-less client redeems it.
type Tickets[T any] struct {
	mu sync.Mutex
	m  map[string]ticket[T]
}

type ticket[T any] struct {
	v   T
	exp time.Time
}

func (t *Tickets[T]) Issue(v T) (string, time.Time) {
	var b [16]byte
	_, _ = rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	exp := time.Now().Add(TicketTTL)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]ticket[T]{}
	}
	now := time.Now()
	for k, x := range t.m {
		if now.After(x.exp) {
			delete(t.m, k)
		}
	}
	t.m[id] = ticket[T]{v: v, exp: exp}
	return id, exp
}

// Take redeems id once.
func (t *Tickets[T]) Take(id string) (T, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	x, ok := t.m[id]
	delete(t.m, id)
	if !ok || time.Now().After(x.exp) {
		var zero T
		return zero, false
	}
	return x.v, true
}
