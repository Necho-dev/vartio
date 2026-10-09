package guest

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type session struct {
	hello      Hello
	conn       *websocket.Conn
	mu         sync.Mutex
	pending    map[string]chan Frame
	termOut    chan []byte
	displayOut chan []byte
	termID     string
	displayID  string
	seen       time.Time
	closed     chan struct{}
	closeOnce  sync.Once
}

func newSession(conn *websocket.Conn, hello Hello) *session {
	return &session{
		hello:      hello,
		conn:       conn,
		pending:    map[string]chan Frame{},
		termOut:    make(chan []byte, 32),
		displayOut: make(chan []byte, 2),
		seen:       time.Now(),
		closed:     make(chan struct{}),
	}
}

func (s *session) info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionInfo{
		Online:      true,
		LastSeenUTC: s.seen.UTC().Format(time.RFC3339),
		Version:     s.hello.Version,
		BiosUUID:    s.hello.BiosUUID,
		HostName:    s.hello.HostName,
		MACs:        append([]string(nil), s.hello.MACs...),
	}
}

func (s *session) touch() {
	s.mu.Lock()
	s.seen = time.Now()
	s.mu.Unlock()
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
		case "out":
			if len(fr.Data) > 0 {
				select {
				case s.termOut <- fr.Data:
				default:
				}
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
