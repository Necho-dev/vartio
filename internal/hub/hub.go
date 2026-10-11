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

	"govmomi-wmks/internal/common"
)

// Hub holds outbound agent sessions keyed by device id (Hello.Key). Control plane
// never dials a guest. VM-scoped callers that only know a BIOS UUID still resolve
// as long as exactly one online session reports it.
type Hub struct {
	Token string
	// Ambiguous is optional. When true for a biosUuid, VM-scoped APIs return guest_unbound.
	Ambiguous func(biosUUID string) bool
	// OnConnect / OnDisconnect / OnReject / OnAudit are optional; set before the hub serves traffic.
	OnConnect    func(SessionInfo)
	OnDisconnect func(SessionInfo)
	// OnReject reports a refused hello: code is device_conflict (the same device id is
	// already live) or an Auth refusal (credential_required, enrollment_disabled, unauthorized).
	OnReject func(hello Hello, remote, code, reason string)
	// OnAudit receives operator actions performed through the probe API.
	OnAudit func(Audit)
	// Releases is where signed agent builds are offered from; nil disables upgrades.
	Releases *Releases
	// Auth signs hellos and binds device credentials; nil keeps the token-only flow.
	Auth *Auth

	mu       sync.Mutex
	sessions map[string]*session
	tickets  map[string]streamTicket
	upgrades map[string]*UpgradeProgress
	fw       forwards
}

// Audit is one operator action on a guest. Err is set when the agent refused or failed it.
type Audit struct {
	Key    string
	Kind   string
	Detail string
	Actor  string
	Err    string
}

type streamTicket struct {
	ID        string
	Key       string
	Kind      string
	Term      TermOpenRequest
	ExpiresAt time.Time
}

func NewHub(token string) *Hub {
	return &Hub{
		Token:    token,
		sessions: map[string]*session{},
		tickets:  map[string]streamTicket{},
		upgrades: map[string]*UpgradeProgress{},
	}
}

func (h *Hub) Online(key string) bool {
	s, _ := h.lookup(key)
	return s != nil
}

func (h *Hub) Info(key string) (SessionInfo, bool) {
	s, _ := h.lookup(key)
	if s == nil {
		return SessionInfo{Online: false, BiosUUID: normUUID(key)}, false
	}
	return s.info(), true
}

func (h *Hub) List() []SessionInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]SessionInfo, 0, len(h.sessions))
	for _, s := range h.sessions {
		out = append(out, s.info())
	}
	return out
}

// Resolve maps a device id or a BIOS UUID to the live session's device id.
func (h *Hub) Resolve(key string) string {
	if s, _ := h.lookup(key); s != nil {
		return s.hello.Key()
	}
	return normUUID(key)
}

// lookup finds a session by device id, then by BIOS UUID. Two online agents sharing
// a BIOS UUID (cloned VMs) make the BIOS form ambiguous.
func (h *Hub) lookup(key string) (*session, error) {
	key = normUUID(key)
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[key]; s != nil {
		return s, nil
	}
	var hit *session
	for _, s := range h.sessions {
		if s.hello.BiosUUID != key {
			continue
		}
		if hit != nil {
			return nil, common.ErrUnbound("biosUuid %q is reported by more than one online agent; use the device id", key)
		}
		hit = s
	}
	return hit, nil
}

// Alive reports whether the session under exactly this device id still answers pings.
func (h *Hub) Alive(key string) bool {
	h.mu.Lock()
	s := h.sessions[normUUID(key)]
	h.mu.Unlock()
	return s != nil && s.alive(3*time.Second)
}

// RTT measures the WebSocket round trip to the agent (ping/pong, no agent work).
func (h *Hub) RTT(key string) (time.Duration, bool) {
	s, _ := h.lookup(key)
	if s == nil {
		return 0, false
	}
	return s.rtt(3 * time.Second)
}

// Kick closes the session under exactly this device id; OnDisconnect follows.
func (h *Hub) Kick(key string) {
	h.mu.Lock()
	s := h.sessions[normUUID(key)]
	h.mu.Unlock()
	if s != nil {
		s.close()
	}
}

func (h *Hub) get(key string) *session {
	s, _ := h.lookup(key)
	return s
}

func (h *Hub) drop(s *session) {
	key := s.hello.Key()
	h.mu.Lock()
	removed := h.sessions[key] == s
	if removed {
		delete(h.sessions, key)
	}
	h.mu.Unlock()
	s.close()
	if removed && h.OnDisconnect != nil {
		info := s.info()
		info.Online = false
		h.OnDisconnect(info)
	}
}

// admit refuses a hello whose device id belongs to a session that still answers
// pings: that is a second machine with the same fingerprint, not a reconnect. A
// half-open old session (agent already gone) is replaced.
func (h *Hub) admit(hello Hello) error {
	h.mu.Lock()
	old := h.sessions[hello.Key()]
	h.mu.Unlock()
	if old == nil || !old.alive(3*time.Second) {
		return nil
	}
	return &RPCError{Code: "device_conflict", Message: "device id " + hello.Key() + " is already connected from " + old.remote}
}

func (h *Hub) accept(conn *websocket.Conn, hello Hello, remote string) *session {
	s := newSession(conn, hello, remote)
	key := hello.Key()
	s.onUpgrade = func(st UpgradeStep) {
		h.progress(key, func(p *UpgradeProgress) { p.Stage, p.Done, p.Total = st.Stage, st.Done, st.Total })
	}
	h.settleUpgrade(key, hello)
	h.mu.Lock()
	old := h.sessions[key]
	h.sessions[key] = s
	h.mu.Unlock()
	if old != nil {
		old.close()
	}
	if h.OnConnect != nil {
		h.OnConnect(s.info())
	}
	go s.readLoop()
	go s.pingLoop()
	go func() {
		<-s.closed
		h.drop(s)
	}()
	return s
}

func (h *Hub) audit(key, kind, detail, actor string, err error) {
	if h.OnAudit == nil {
		return
	}
	a := Audit{Key: h.Resolve(key), Kind: kind, Detail: detail, Actor: actor}
	if err != nil {
		a.Err = err.Error()
	}
	h.OnAudit(a)
}

func (h *Hub) CheckBound(biosUUID string) error {
	if h.Ambiguous != nil && h.Ambiguous(biosUUID) {
		return common.ErrUnbound("guest agent biosUuid %q is unbound or ambiguous; use display name or moref", biosUUID)
	}
	return nil
}

func (h *Hub) Call(ctx context.Context, key, method string, params any) (json.RawMessage, error) {
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

// IssueTerm hands out a terminal ticket. target picks a container's logs or a
// shell in it (Mode, Container, Shell, Tail); the zero value is the agent's shell.
func (h *Hub) IssueTerm(key string, target TermOpenRequest) (map[string]string, error) {
	target = TermOpenRequest{Mode: target.Mode, Container: strings.TrimSpace(target.Container), Shell: strings.TrimSpace(target.Shell), Tail: target.Tail, Since: target.Since}
	switch target.Mode {
	case "":
		target = TermOpenRequest{}
	case TermLogs, TermExec:
		if target.Container == "" {
			return nil, common.ErrBad("container required")
		}
	default:
		return nil, common.ErrBad("mode must be logs or exec")
	}
	return h.issueStream(key, "term", PathTermWS, "open in a browser; one-shot, expires in 2 minutes. view/type shell only. ESXi WSS is never used.", func(s *session) (TermOpenRequest, error) {
		switch {
		case target.Mode == "":
		case !s.hello.Has(CapDocker):
			return target, errCapMissing(CapDocker)
		case target.Mode == TermExec && !s.hello.Has(CapDockerExec):
			return target, &common.Error{Status: http.StatusForbidden, Code: "agent_policy", Message: "探针未开放容器终端（需 GUEST_DOCKER=rw）"}
		}
		return target, nil
	})
}

func (h *Hub) IssueDisplay(key string) (map[string]string, error) {
	return h.issueStream(key, "display", PathDisplayWS, "view-only JPEG up to 10fps (quality=auto|smooth|hd on the ws URL); no input. one-shot, expires in 2 minutes. ESXi WSS is never used.", nil)
}

func (h *Hub) issueStream(key, kind, path, note string, check func(*session) (TermOpenRequest, error)) (map[string]string, error) {
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
	var target TermOpenRequest
	if check != nil {
		if target, err = check(s); err != nil {
			return nil, err
		}
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	now := time.Now()
	for k, t := range h.tickets {
		if now.After(t.ExpiresAt) {
			delete(h.tickets, k)
		}
	}
	h.tickets[id] = streamTicket{ID: id, Key: s.hello.Key(), Kind: kind, Term: target, ExpiresAt: time.Now().Add(TicketTTL)}
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
