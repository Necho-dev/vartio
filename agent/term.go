package main

import (
	"io"
	"sync"

	"govmomi-wmks/internal/guest"
)

type termHub struct {
	mu     sync.Mutex
	stdin  io.WriteCloser
	closer func()
}

func newTermHub() *termHub { return &termHub{} }

func (t *termHub) open(req guest.TermOpenRequest, onOut func([]byte)) error {
	t.close()
	cols, rows := req.Cols, req.Rows
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 32
	}
	stdin, closer, err := startPTY(cols, rows, onOut)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.stdin = stdin
	t.closer = closer
	t.mu.Unlock()
	return nil
}

func (t *termHub) write(p []byte) {
	t.mu.Lock()
	w := t.stdin
	t.mu.Unlock()
	if w != nil && len(p) > 0 {
		_, _ = w.Write(p)
	}
}

func (t *termHub) close() {
	t.mu.Lock()
	c := t.closer
	t.stdin = nil
	t.closer = nil
	t.mu.Unlock()
	if c != nil {
		c()
	}
}
