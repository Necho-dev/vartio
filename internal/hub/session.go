package guest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type session struct {
	hello      Hello
	conn       *websocket.Conn
	mu         sync.Mutex
	pending    map[string]chan Frame
	terms      map[string]*termSink
	displayOut chan []byte
	displayID  string
	seen       time.Time
	remote     string
	since      time.Time
	closed     chan struct{}
	closeOnce  sync.Once
}

func newSession(conn *websocket.Conn, hello Hello, remote string) *session {
	now := time.Now()
	return &session{
		hello:      hello,
		conn:       conn,
		pending:    map[string]chan Frame{},
		terms:      map[string]*termSink{},
		displayOut: make(chan []byte, 2),
		seen:       now,
		remote:     remote,
		since:      now,
		closed:     make(chan struct{}),
	}
}

// termStall bounds how long a slow browser may stall the agent read loop before
// terminal output is dropped.
const termStall = 2 * time.Second

// termSink receives "out" frames for one browser terminal. bye is shown to the
// browser when the sink is stopped from outside (taken over / shell exited).
type termSink struct {
	id   string
	out  chan []byte
	done chan struct{}
	once sync.Once
	bye  string
}

func (t *termSink) stop(bye string) {
	t.once.Do(func() {
		t.bye = bye
		close(t.done)
	})
}

// attachTerm registers a terminal stream. Single-PTY agents get the previous
// terminal replaced; multi-PTY agents are capped at MaxTerms.
func (s *session) attachTerm(id string) (*termSink, error) {
	t := &termSink{id: id, out: make(chan []byte, 256), done: make(chan struct{})}
	var evicted []*termSink
	s.mu.Lock()
	if s.hello.Has(CapMultiTerm) {
		if len(s.terms) >= MaxTerms {
			s.mu.Unlock()
			return nil, &RPCError{Code: "too_many_terms", Message: fmt.Sprintf("at most %d terminals per guest", MaxTerms)}
		}
	} else {
		for k, v := range s.terms {
			evicted = append(evicted, v)
			delete(s.terms, k)
		}
	}
	s.terms[id] = t
	s.mu.Unlock()
	for _, v := range evicted {
		v.stop("已在其他窗口打开终端（该探针版本只支持单会话）")
	}
	return t, nil
}

// detachTerm reports whether t was still registered, i.e. the agent PTY is
// still ours to close.
func (s *session) detachTerm(t *termSink) bool {
	s.mu.Lock()
	active := s.terms[t.id] == t
	if active {
		delete(s.terms, t.id)
	}
	s.mu.Unlock()
	t.stop("")
	return active
}

func (s *session) termFor(id string) *termSink {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terms[id]
}

func (s *session) info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionInfo{
		Online:      true,
		LastSeenUTC: s.seen.UTC().Format(time.RFC3339),
		Version:     s.hello.Version,
		DeviceID:    s.hello.Key(),
		Device:      s.hello.Device,
		BiosUUID:    s.hello.BiosUUID,
		HostName:    s.hello.HostName,
		MACs:        append([]string(nil), s.hello.MACs...),
		RemoteAddr:  s.remote,
		ConnectedAt: s.since.UTC().Format(time.RFC3339),
		Caps:        append([]string(nil), s.hello.Caps...),
		OS:          s.hello.OS,
		Arch:        s.hello.Arch,
		Runner:      s.hello.Runner,
		Upgrade:     s.hello.Upgrade,
	}
}

func (s *session) touch() {
	s.mu.Lock()
	s.seen = time.Now()
	s.mu.Unlock()
}

// alive pings the agent and reports whether any frame or pong arrives within d.
func (s *session) alive(d time.Duration) bool {
	s.mu.Lock()
	t0 := time.Now()
	err := s.conn.WriteControl(websocket.PingMessage, nil, t0.Add(d))
	s.mu.Unlock()
	if err != nil {
		return false
	}
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.closed:
			return false
		case <-deadline.C:
			return false
		case <-tick.C:
			s.mu.Lock()
			ok := s.seen.After(t0)
			s.mu.Unlock()
			if ok {
				return true
			}
		}
	}
}

func (s *session) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.conn.Close()
		s.mu.Lock()
		for id, ch := range s.pending {
			close(ch)
			delete(s.pending, id)
		}
		s.mu.Unlock()
	})
}

func (s *session) write(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return s.conn.WriteJSON(v)
}

func (s *session) call(ctx context.Context, id, method string, params any) (json.RawMessage, error) {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	ch := make(chan Frame, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	if err := s.write(Frame{ID: id, Method: method, Params: raw}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, errOffline
	case fr, ok := <-ch:
		if !ok {
			return nil, errOffline
		}
		if fr.Error != nil {
			return nil, &RPCError{Code: fr.Error.Code, Message: fr.Error.Message}
		}
		return fr.Result, nil
	}
}

func (s *session) deliver(fr Frame) {
	s.touch()
	if fr.Stream != "" {
		switch fr.Stream {
		case StreamTermExit:
			if t := s.termFor(fr.ID); t != nil {
				s.mu.Lock()
				delete(s.terms, fr.ID)
				s.mu.Unlock()
				t.stop("shell 已退出")
			}
		case "out":
			if len(fr.Data) == 0 {
				return
			}
			t := s.termFor(fr.ID)
			if t == nil {
				return
			}
			select {
			case t.out <- fr.Data:
			case <-t.done:
			case <-time.After(termStall):
			}
		case "display":
			if len(fr.Data) == 0 {
				return
			}
			select {
			case s.displayOut <- fr.Data:
			default:
				select {
				case <-s.displayOut:
				default:
				}
				select {
				case s.displayOut <- fr.Data:
				default:
				}
			}
		}
		return
	}
	s.mu.Lock()
	ch := s.pending[fr.ID]
	s.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- fr:
	default:
	}
}

func (s *session) readLoop() {
	defer s.close()
	s.conn.SetReadLimit(8 << 20)
	_ = s.conn.SetReadDeadline(time.Now().Add(PongWait))
	s.conn.SetPongHandler(func(string) error {
		s.touch()
		return s.conn.SetReadDeadline(time.Now().Add(PongWait))
	})
	for {
		var fr Frame
		if err := s.conn.ReadJSON(&fr); err != nil {
			return
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(PongWait))
		s.deliver(fr)
	}
}

func (s *session) pingLoop() {
	t := time.NewTicker(PingEvery)
	defer t.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-t.C:
			s.mu.Lock()
			err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			s.mu.Unlock()
			if err != nil {
				s.close()
				return
			}
		}
	}
}
