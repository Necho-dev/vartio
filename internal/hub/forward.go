package guest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"govmomi-wmks/internal/common"
)

// Forward publishes a TCP service on an agent's machine as a port on the hub
// machine. Every accepted connection opens its own agent tunnel, so HTTP,
// WebSocket, SSH, databases… all pass through unchanged.
type Forward struct {
	ID string `json:"id"`
	// Agent is the device id (BIOS UUID for old agents); Name is the hostname
	// at creation, shown while the agent is offline.
	Agent   string    `json:"agent"`
	Name    string    `json:"name,omitempty"`
	Bind    string    `json:"bind"`
	Port    int       `json:"port"`
	Target  string    `json:"target"`
	Note    string    `json:"note,omitempty"`
	Created time.Time `json:"created"`
}

type ForwardInfo struct {
	Forward
	Online    bool   `json:"online"`
	Listening bool   `json:"listening"`
	Error     string `json:"error,omitempty"`
	LastError string `json:"lastError,omitempty"`
	Conns     int64  `json:"conns"`
	Total     int64  `json:"total"`
	RxBytes   int64  `json:"rxBytes"`
	TxBytes   int64  `json:"txBytes"`
}

type ForwardRequest struct {
	Agent  string `json:"agent"`
	Bind   string `json:"bind"`
	Port   int    `json:"port"`
	Target string `json:"target"`
	Note   string `json:"note"`
}

// ForwardBinds are the listen addresses offered; 0.0.0.0 exposes the remote
// service to the hub's network without any auth.
var ForwardBinds = []string{"127.0.0.1", "0.0.0.0"}

// MaxForwards caps forward rules per hub.
const MaxForwards = 200

type forward struct {
	Forward
	ln       net.Listener
	err      string
	lastErr  atomic.Value
	conns    atomic.Int64
	total    atomic.Int64
	rx, tx   atomic.Int64
	stopping atomic.Bool
}

type forwards struct {
	mu   sync.Mutex
	list []*forward
	path string
}

func (f *forward) owner() string { return "forward:" + f.ID }

func (f *forward) info(h *Hub) ForwardInfo {
	out := ForwardInfo{Forward: f.Forward, Online: h.Online(f.Agent), Listening: f.ln != nil, Error: f.err,
		Conns: f.conns.Load(), Total: f.total.Load(), RxBytes: f.rx.Load(), TxBytes: f.tx.Load()}
	if v, ok := f.lastErr.Load().(string); ok {
		out.LastError = v
	}
	if info, ok := h.Info(f.Agent); ok && info.HostName != "" {
		out.Name = info.HostName
	}
	return out
}

func errPortTaken(format string, args ...any) error {
	return &common.Error{Status: http.StatusConflict, Code: "port_taken", Message: fmt.Sprintf(format, args...)}
}

// portBusy reports whether something on this machine already accepts on
// port. Go listeners set SO_REUSEADDR, so on macOS/BSD binding 127.0.0.1:port
// can succeed next to a *:port server and silently steal its local traffic.
func portBusy(port int) bool {
	for _, a := range []string{"127.0.0.1", "[::1]"} {
		c, err := net.DialTimeout("tcp", a+":"+strconv.Itoa(port), 300*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return true
		}
	}
	return false
}

func normForward(req *ForwardRequest) error {
	req.Agent = strings.TrimSpace(req.Agent)
	req.Target = strings.TrimSpace(req.Target)
	req.Bind = strings.TrimSpace(req.Bind)
	req.Note = strings.TrimSpace(req.Note)
	if req.Agent == "" {
		return common.ErrBad("agent is required")
	}
	if req.Bind == "" {
		req.Bind = ForwardBinds[0]
	}
	if !slices.Contains(ForwardBinds, req.Bind) {
		return common.ErrBad("bind must be one of %s", strings.Join(ForwardBinds, ", "))
	}
	if req.Port < 0 || req.Port > 65535 {
		return common.ErrBad("port must be 1-65535, or 0 for any free port")
	}
	host, port, err := net.SplitHostPort(req.Target)
	if err != nil || host == "" {
		return common.ErrBad("target must be host:port, e.g. 127.0.0.1:8080")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return common.ErrBad("target port must be 1-65535")
	}
	if len(req.Note) > 200 {
		req.Note = req.Note[:200]
	}
	return nil
}

// AddForward validates the rule, test-dials the target through the agent,
// and starts listening. Local ports are unique across all rules.
func (h *Hub) AddForward(ctx context.Context, req ForwardRequest) (ForwardInfo, error) {
	if err := normForward(&req); err != nil {
		return ForwardInfo{}, err
	}
	s, err := h.lookup(req.Agent)
	if err != nil {
		return ForwardInfo{}, err
	}
	if s == nil {
		return ForwardInfo{}, common.ErrOffline()
	}
	info := s.info()
	if !s.hello.Has(CapTunnelTCP) {
		return ForwardInfo{}, errCapMissing(CapTunnelTCP)
	}
	h.fw.mu.Lock()
	err = h.portFreeLocked(req.Port)
	h.fw.mu.Unlock()
	if err != nil {
		return ForwardInfo{}, err
	}
	probe, err := h.dialTCP(ctx, info.DeviceID, req.Target, "forward:test", TunnelIdle)
	if err != nil {
		return ForwardInfo{}, err
	}
	_ = probe.Close()

	id, err := randomID()
	if err != nil {
		return ForwardInfo{}, err
	}
	f := &forward{Forward: Forward{ID: id[:12], Agent: info.DeviceID, Name: info.HostName, Bind: req.Bind, Port: req.Port,
		Target: req.Target, Note: req.Note, Created: time.Now().UTC()}}

	h.fw.mu.Lock()
	defer h.fw.mu.Unlock()
	if len(h.fw.list) >= MaxForwards {
		return ForwardInfo{}, &common.Error{Status: http.StatusConflict, Code: "too_many_forwards", Message: fmt.Sprintf("at most %d forwards", MaxForwards)}
	}
	if err := h.portFreeLocked(f.Port); err != nil {
		return ForwardInfo{}, err
	}
	if err := h.listenForward(f); err != nil {
		return ForwardInfo{}, err
	}
	h.fw.list = append(h.fw.list, f)
	h.saveForwardsLocked()
	return f.info(h), nil
}

// portFreeLocked checks port against every rule and this machine's listeners;
// 0 (pick one) always passes. Called again after the test dial, under the lock
// that also covers listening, so two requests cannot take the same port.
func (h *Hub) portFreeLocked(port int) error {
	if port == 0 {
		return nil
	}
	for _, o := range h.fw.list {
		if o.Port == port {
			return errPortTaken("本地端口 %d 已被转发 %s → %s 使用", port, o.Name, o.Target)
		}
	}
	if portBusy(port) {
		return errPortTaken("本地端口 %d 已被本机其他程序占用", port)
	}
	return nil
}

// listenForward binds f; with Port 0 the OS picks one that no rule holds.
func (h *Hub) listenForward(f *forward) error {
	for range 20 {
		ln, err := net.Listen("tcp", net.JoinHostPort(f.Bind, strconv.Itoa(f.Port)))
		if err != nil {
			return errPortTaken("无法监听 %s:%d：%v", f.Bind, f.Port, errors.Unwrap(err))
		}
		port := ln.Addr().(*net.TCPAddr).Port
		if f.Port == 0 && slices.ContainsFunc(h.fw.list, func(o *forward) bool { return o.Port == port }) {
			_ = ln.Close()
			continue
		}
		f.Port, f.ln, f.err = port, ln, ""
		go h.acceptForward(f, ln)
		return nil
	}
	return errPortTaken("没有可用的本地端口")
}

func (h *Hub) acceptForward(f *forward, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if !f.stopping.Load() {
				log.Printf("forward %s :%d stopped: %v", f.ID, f.Port, err)
			}
			return
		}
		go h.serveForward(f, c)
	}
}

func (h *Hub) serveForward(f *forward, c net.Conn) {
	defer c.Close()
	f.total.Add(1)
	f.conns.Add(1)
	defer f.conns.Add(-1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	up, err := h.dialTCP(ctx, f.Agent, f.Target, f.owner(), 0)
	cancel()
	if err != nil {
		f.lastErr.Store(time.Now().Format("15:04:05") + " " + err.Error())
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	pump := func(dst, src net.Conn, n *atomic.Int64) {
		buf := make([]byte, TunnelChunk)
		for {
			k, err := src.Read(buf)
			if k > 0 {
				n.Add(int64(k))
				if _, werr := dst.Write(buf[:k]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}
	go pump(up, c, &f.tx)
	go pump(c, up, &f.rx)
	<-done
}

// RemoveForward stops listening and drops the rule's live connections.
func (h *Hub) RemoveForward(id string) (Forward, error) {
	h.fw.mu.Lock()
	i := slices.IndexFunc(h.fw.list, func(f *forward) bool { return f.ID == id })
	if i < 0 {
		h.fw.mu.Unlock()
		return Forward{}, &common.Error{Status: http.StatusNotFound, Code: "forward_not_found", Message: "no forward " + id}
	}
	f := h.fw.list[i]
	h.fw.list = slices.Delete(h.fw.list, i, i+1)
	h.saveForwardsLocked()
	h.fw.mu.Unlock()
	f.stopping.Store(true)
	if f.ln != nil {
		_ = f.ln.Close()
	}
	h.CloseTunnels(f.owner())
	return f.Forward, nil
}

// Forwards lists rules, all of them when key is empty, else one agent's.
func (h *Hub) Forwards(key string) []ForwardInfo {
	want := ""
	if key != "" {
		want = key
		if info, ok := h.Info(key); ok {
			want = info.DeviceID
		}
	}
	h.fw.mu.Lock()
	defer h.fw.mu.Unlock()
	out := []ForwardInfo{}
	for _, f := range h.fw.list {
		if want == "" || f.Agent == want || f.Agent == normUUID(key) {
			out = append(out, f.info(h))
		}
	}
	return out
}

type forwardState struct {
	Forwards []Forward `json:"forwards"`
}

// LoadForwards restores saved rules and remembers path for later changes. A
// rule whose port cannot be bound any more is kept with Error set.
func (h *Hub) LoadForwards(path string) error {
	h.fw.mu.Lock()
	defer h.fw.mu.Unlock()
	h.fw.path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var st forwardState
	if err := json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, fw := range st.Forwards {
		f := &forward{Forward: fw}
		if f.Port == 0 {
			continue
		}
		if err := h.listenForward(f); err != nil {
			f.err = err.Error()
			log.Printf("forward %s :%d -> %s: %v", f.ID, f.Port, f.Target, err)
		}
		h.fw.list = append(h.fw.list, f)
	}
	return nil
}

func (h *Hub) saveForwardsLocked() {
	if h.fw.path == "" {
		return
	}
	st := forwardState{Forwards: []Forward{}}
	for _, f := range h.fw.list {
		st.Forwards = append(st.Forwards, f.Forward)
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp := h.fw.path + ".tmp"
	if dir := filepath.Dir(h.fw.path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("save forwards: %v", err)
		return
	}
	if err := os.Rename(tmp, h.fw.path); err != nil {
		log.Printf("save forwards: %v", err)
	}
}

// ---------- HTTP ----------

func (h *Hub) registerForwards(mux *http.ServeMux) {
	mux.HandleFunc("GET "+PathForwards, h.wrap(func(w http.ResponseWriter, r *http.Request) error {
		return writeJSON(w, map[string]any{"forwards": h.Forwards(r.URL.Query().Get("agent")), "binds": ForwardBinds})
	}))
	mux.HandleFunc("POST "+PathForwards, h.wrap(func(w http.ResponseWriter, r *http.Request) error {
		var req ForwardRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
			return common.ErrBad("invalid json")
		}
		out, err := h.AddForward(r.Context(), req)
		detail := fmt.Sprintf("%s:%d -> %s", out.Bind, out.Port, out.Target)
		if err != nil {
			port := "自动"
			if req.Port > 0 {
				port = strconv.Itoa(req.Port)
			}
			detail = fmt.Sprintf("本地端口 %s -> %s", port, req.Target)
		}
		h.Record(r, req.Agent, "tunnel.forward", detail, err)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		return json.NewEncoder(w).Encode(out)
	}))
	mux.HandleFunc("DELETE "+PathForwards+"/{fid}", h.wrap(func(w http.ResponseWriter, r *http.Request) error {
		f, err := h.RemoveForward(r.PathValue("fid"))
		if err != nil {
			return err
		}
		h.Record(r, f.Agent, "tunnel.unforward", fmt.Sprintf("%s:%d -> %s", f.Bind, f.Port, f.Target), nil)
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
}
