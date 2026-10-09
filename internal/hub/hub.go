package guest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"govmomi-wmks/internal/esxi"
)

// Hub holds outbound agent sessions. Control plane never dials a guest.
type Hub struct {
	Token string
	// Ambiguous is optional. When true for a biosUuid, VM-scoped APIs return guest_unbound.
	Ambiguous func(biosUUID string) bool

	mu      sync.Mutex
	byBIOS  map[string]*session
	tickets map[string]streamTicket
}

type streamTicket struct {
	ID        string
	BiosUUID  string
	Kind      string
	ExpiresAt time.Time
}

func NewHub(token string) *Hub {
	return &Hub{
		Token:   token,
		byBIOS:  map[string]*session{},
		tickets: map[string]streamTicket{},
	}
}

func (h *Hub) Online(biosUUID string) bool {
	biosUUID = normUUID(biosUUID)
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.byBIOS[biosUUID]
	return ok
}

func (h *Hub) Info(biosUUID string) (SessionInfo, bool) {
	s := h.get(biosUUID)
	if s == nil {
		return SessionInfo{Online: false, BiosUUID: normUUID(biosUUID)}, false
	}
	return s.info(), true
}

func (h *Hub) List() []SessionInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]SessionInfo, 0, len(h.byBIOS))
	for _, s := range h.byBIOS {
		out = append(out, s.info())
	}
	return out
}

func (h *Hub) get(biosUUID string) *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byBIOS[normUUID(biosUUID)]
}

func (h *Hub) drop(s *session) {
	h.mu.Lock()
	cur := h.byBIOS[normUUID(s.hello.BiosUUID)]
	if cur == s {
		delete(h.byBIOS, normUUID(s.hello.BiosUUID))
	}
	h.mu.Unlock()
	s.close()
}

func (h *Hub) accept(conn *websocket.Conn, hello Hello) *session {
	hello.BiosUUID = normUUID(hello.BiosUUID)
	s := newSession(conn, hello)
	h.mu.Lock()
	if old := h.byBIOS[hello.BiosUUID]; old != nil {
		old.close()
	}
	h.byBIOS[hello.BiosUUID] = s
	h.mu.Unlock()
	go s.readLoop()
	go s.pingLoop()
	go func() {
		<-s.closed
		h.drop(s)
	}()
	return s
}

func (h *Hub) CheckBound(biosUUID string) error {
	if h.Ambiguous != nil && h.Ambiguous(biosUUID) {
		return esxi.ErrGuestUnbound("guest agent biosUuid %q is unbound or ambiguous; use display name or moref", biosUUID)
	}
	return nil
}

func (h *Hub) Call(ctx context.Context, biosUUID, method string, params any) (json.RawMessage, error) {
	if err := h.CheckBound(biosUUID); err != nil {
		return nil, err
	}
	s := h.get(biosUUID)
	if s == nil {
		return nil, esxi.ErrGuestOffline()
	}
	execSec := 0
	if req, ok := params.(ExecRequest); ok {
		execSec = req.TimeoutSec
	}
	timeout := TimeoutFor(method, execSec)
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > timeout {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	raw, err := s.call(ctx, id, method, params)
	if err != nil {
		return nil, APIError(err)
	}
	return raw, nil
}

func (h *Hub) IssueTerm(biosUUID string) (map[string]string, error) {
	return h.issueStream(biosUUID, "term", PathTermWS, "open in a browser; one-shot, expires in 2 minutes. view/type shell only. ESXi WSS is never used.")
}

func (h *Hub) IssueDisplay(biosUUID string) (map[string]string, error) {
	return h.issueStream(biosUUID, "display", PathDisplayWS, "view-only JPEG ~2fps, max side 1920; no input. one-shot, expires in 2 minutes. ESXi WSS is never used.")
}

func (h *Hub) issueStream(biosUUID, kind, path, note string) (map[string]string, error) {
	if err := h.CheckBound(biosUUID); err != nil {
		return nil, err
	}
	if h.get(biosUUID) == nil {
		return nil, esxi.ErrGuestOffline()
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.tickets[id] = streamTicket{ID: id, BiosUUID: normUUID(biosUUID), Kind: kind, ExpiresAt: time.Now().Add(TicketTTL)}
	h.mu.Unlock()
	return map[string]string{
		"ws":   path + "?id=" + id,
		"note": note,
	}, nil
}

func (h *Hub) takeTicket(id, kind string) (streamTicket, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.tickets[id]
	if !ok || t.Kind != kind {
		return streamTicket{}, false
	}
	delete(h.tickets, id)
	if time.Now().After(t.ExpiresAt) {
		return streamTicket{}, false
	}
	return t, true
}

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, nil
	}
	err := json.Unmarshal(raw, &v)
	return v, err
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func normUUID(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func tokenOK(want, got string) bool {
	if want == "" {
		return true
	}
	return got == want
}

func guestToken(r *http.Request) string {
	got := strings.TrimSpace(r.Header.Get("X-Guest-Token"))
	if got == "" {
		got = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	return strings.TrimSpace(got)
}
