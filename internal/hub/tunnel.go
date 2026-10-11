package guest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"govmomi-wmks/internal/common"
)

// tunnelStall bounds how long a consumer that stopped reading may hold up the
// session read loop; the tunnel is closed rather than dropping bytes.
const tunnelStall = 5 * time.Second

// tunnelSink is the hub end of one agent TCP tunnel.
type tunnelSink struct {
	id         string
	target     string
	owner      string
	opened     time.Time
	in         chan []byte
	done       chan struct{}
	once       sync.Once
	peerClosed atomic.Bool
	last       atomic.Int64
	rx, tx     atomic.Int64
	idle       time.Duration // 0 keeps a silent tunnel open
}

type TunnelInfo struct {
	ID      string    `json:"id"`
	Target  string    `json:"target"`
	Owner   string    `json:"owner,omitempty"`
	Opened  time.Time `json:"opened"`
	RxBytes int64     `json:"rxBytes"`
	TxBytes int64     `json:"txBytes"`
}

func (t *tunnelSink) stop() { t.once.Do(func() { close(t.done) }) }

func (t *tunnelSink) touch() { t.last.Store(time.Now().UnixNano()) }

func (t *tunnelSink) push(p []byte) {
	select {
	case t.in <- p:
	case <-t.done:
	case <-time.After(tunnelStall):
		t.stop()
	}
}

func (t *tunnelSink) info() TunnelInfo {
	return TunnelInfo{ID: t.id, Target: t.target, Owner: t.owner, Opened: t.opened, RxBytes: t.rx.Load(), TxBytes: t.tx.Load()}
}

func (s *session) attachTunnel(id, target, owner string, idle time.Duration) (*tunnelSink, error) {
	t := &tunnelSink{id: id, target: target, owner: owner, idle: idle, opened: time.Now(), in: make(chan []byte, 256), done: make(chan struct{})}
	t.touch()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tunnels) >= MaxTunnels {
		return nil, &common.Error{Status: http.StatusConflict, Code: "too_many_tunnels", Message: fmt.Sprintf("at most %d tunnels per guest", MaxTunnels)}
	}
	s.tunnels[id] = t
	return t, nil
}

// detachTunnel reports whether t was still registered.
func (s *session) detachTunnel(t *tunnelSink) bool {
	s.mu.Lock()
	active := s.tunnels[t.id] == t
	if active {
		delete(s.tunnels, t.id)
	}
	s.mu.Unlock()
	t.stop()
	return active
}

func (s *session) tunnelFor(id string) *tunnelSink {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tunnels[id]
}

// run relays between the caller's pipe end and the agent until either side closes.
func (t *tunnelSink) run(s *session, p net.Conn) {
	defer func() {
		_ = p.Close()
		if s.detachTunnel(t) && !t.peerClosed.Load() {
			_ = s.write(Frame{ID: t.id, Stream: StreamTunnelClose})
		}
	}()
	go func() {
		buf := make([]byte, TunnelChunk)
		for {
			n, err := p.Read(buf)
			if n > 0 {
				t.touch()
				t.tx.Add(int64(n))
				if s.write(Frame{ID: t.id, Stream: StreamTunnel, Data: append([]byte(nil), buf[:n]...)}) != nil {
					t.stop()
					return
				}
			}
			if err != nil {
				t.stop()
				return
			}
		}
	}()
	write := func(b []byte) bool {
		t.touch()
		t.rx.Add(int64(len(b)))
		_ = p.SetWriteDeadline(time.Now().Add(TunnelIdle))
		_, err := p.Write(b)
		return err == nil
	}
	idle := time.NewTicker(time.Minute)
	defer idle.Stop()
	for {
		select {
		case b := <-t.in:
			if !write(b) {
				return
			}
		case <-t.done:
			for {
				select {
				case b := <-t.in:
					if !write(b) {
						return
					}
				default:
					return
				}
			}
		case <-idle.C:
			if t.idle > 0 && time.Since(time.Unix(0, t.last.Load())) > t.idle {
				return
			}
		}
	}
}

func errCapMissing(c string) error {
	return &common.Error{Status: http.StatusConflict, Code: "cap_missing", Message: "agent does not support " + c + "; upgrade the probe agent"}
}

// DialTCP opens a TCP connection from the agent to target (host:port as the
// agent sees it). The agent's local policy decides which targets are allowed;
// loopback only by default. owner labels the tunnel in listings and audit.
func (h *Hub) DialTCP(ctx context.Context, key, target, owner string) (net.Conn, error) {
	return h.dialTCP(ctx, key, target, owner, TunnelIdle)
}

func (h *Hub) dialTCP(ctx context.Context, key, target, owner string, idle time.Duration) (net.Conn, error) {
	if _, _, err := net.SplitHostPort(target); err != nil {
		return nil, common.ErrBad("target must be host:port")
	}
	if err := h.CheckBound(key); err != nil {
		return nil, err
	}
	s, err := h.lookup(key)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, common.ErrOffline()
	}
	if !s.hello.Has(CapTunnelTCP) {
		return nil, errCapMissing(CapTunnelTCP)
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	t, err := s.attachTunnel(id, target, owner, idle)
	if err != nil {
		return nil, err
	}
	c, cancel := context.WithTimeout(ctx, TimeoutFor(MethodTunnelOpen, 0))
	_, err = s.call(c, id, MethodTunnelOpen, TunnelOpenRequest{ID: id, Mode: "tcp", Target: target})
	cancel()
	if err != nil {
		s.detachTunnel(t)
		return nil, APIError(err)
	}
	local, remote := net.Pipe()
	go t.run(s, remote)
	return local, nil
}

// Tunnels lists the open TCP tunnels of one guest.
func (h *Hub) Tunnels(key string) []TunnelInfo {
	s, _ := h.lookup(key)
	out := []TunnelInfo{}
	if s == nil {
		return out
	}
	s.mu.Lock()
	for _, t := range s.tunnels {
		out = append(out, t.info())
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Opened.Before(out[j].Opened) })
	return out
}

// CloseTunnels closes every tunnel owner opened, on all guests.
func (h *Hub) CloseTunnels(owner string) int {
	h.mu.Lock()
	var hit []*tunnelSink
	for _, s := range h.sessions {
		s.mu.Lock()
		for _, t := range s.tunnels {
			if t.owner == owner {
				hit = append(hit, t)
			}
		}
		s.mu.Unlock()
	}
	h.mu.Unlock()
	for _, t := range hit {
		t.stop()
	}
	return len(hit)
}

// CallPrimitive invokes an RPC-backed primitive (PrimitiveMethods), refusing
// agents that do not declare it.
func (h *Hub) CallPrimitive(ctx context.Context, key, primitive string, params any) (json.RawMessage, error) {
	method, ok := PrimitiveMethods[primitive]
	if !ok {
		return nil, common.ErrBad("%s is not an RPC primitive", primitive)
	}
	s, err := h.lookup(key)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, common.ErrOffline()
	}
	if !s.hello.Has(primitive) {
		return nil, errCapMissing(primitive)
	}
	return h.Call(ctx, key, method, params)
}
