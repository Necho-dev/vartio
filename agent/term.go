package main

import (
	"fmt"
	"io"
	"sync"

	"govmomi-wmks/internal/guest"
)

// termHub runs one PTY per hub stream id (one per browser tab).
type termHub struct {
	mu    sync.Mutex
	terms map[string]*ptySession
}

type ptySession struct {
	stdin  io.WriteCloser
	closer func()
	resize func(cols, rows int)
}

func newTermHub() *termHub { return &termHub{terms: map[string]*ptySession{}} }

// open starts a shell for id; onExit fires once if the shell ends on its own.
func (t *termHub) open(id string, req guest.TermOpenRequest, onOut func([]byte), onExit func()) error {
	t.close(id)
	ps := &ptySession{}
	exited := func() {
		t.mu.Lock()
		mine := t.terms[id] == ps
		if mine {
			delete(t.terms, id)
		}
		t.mu.Unlock()
		if mine {
			onExit()
		}
	}
	// Held across startPTY so an instant shell exit (callbacks run on their own
	// goroutines) still finds the session registered.
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.terms) >= guest.MaxTerms {
		return fmt.Errorf("at most %d terminals", guest.MaxTerms)
	}
	cols, rows := clampSize(req.Cols, req.Rows)
	var (
		stdin  io.WriteCloser
		closer func()
		resize func(int, int)
		err    error
	)
	switch req.Mode {
	case "":
		stdin, closer, resize, err = startPTY(cols, rows, onOut, exited)
	case guest.TermLogs:
		stdin, closer, resize, err = startDockerLogs(req, onOut, exited)
	case guest.TermExec:
		stdin, closer, resize, err = startDockerExec(req, cols, rows, onOut, exited)
	default:
		err = &guest.FrameError{Code: "bad_request", Message: "unknown terminal mode " + req.Mode}
	}
	if err != nil {
		return err
	}
	ps.stdin, ps.closer, ps.resize = stdin, closer, resize
	t.terms[id] = ps
	return nil
}

func (t *termHub) write(id string, p []byte) {
	t.mu.Lock()
	ps := t.terms[id]
	t.mu.Unlock()
	if ps != nil && len(p) > 0 {
		_, _ = ps.stdin.Write(p)
	}
}

// setSize resizes id; an empty id (older hubs) resizes every terminal.
func (t *termHub) setSize(id string, cols, rows int) {
	cols, rows = clampSize(cols, rows)
	for _, ps := range t.pick(id) {
		ps.resize(cols, rows)
	}
}

// close stops the PTY owned by id; an empty id closes all of them.
func (t *termHub) close(id string) {
	t.mu.Lock()
	list := t.pickLocked(id)
	for k, v := range t.terms {
		for _, ps := range list {
			if v == ps {
				delete(t.terms, k)
			}
		}
	}
	t.mu.Unlock()
	for _, ps := range list {
		ps.closer()
	}
}

func (t *termHub) pick(id string) []*ptySession {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pickLocked(id)
}

func (t *termHub) pickLocked(id string) []*ptySession {
	if id != "" {
		if ps := t.terms[id]; ps != nil {
			return []*ptySession{ps}
		}
		return nil
	}
	out := make([]*ptySession, 0, len(t.terms))
	for _, ps := range t.terms {
		out = append(out, ps)
	}
	return out
}

func clampSize(cols, rows int) (int, int) {
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 32
	}
	return min(cols, 500), min(rows, 200)
}
