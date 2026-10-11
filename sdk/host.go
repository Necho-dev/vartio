package sdk

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	PathPrefix   = "/v1/probe/plugins"
	PublicPrefix = "/v1/probe/ws/plugins"
	UIPrefix     = "/plugins"
	TicketTTL    = 2 * time.Minute
)

// SessionInfo is the agent view the hub returns to a plugin.
type SessionInfo struct {
	Online     bool           `json:"online"`
	Version    string         `json:"version,omitempty"`
	DeviceID   string         `json:"deviceId,omitempty"`
	BiosUUID   string         `json:"biosUuid,omitempty"`
	HostName   string         `json:"hostname,omitempty"`
	Primitives map[string]int `json:"primitives,omitempty"`
	Caps       []string       `json:"caps,omitempty"`
	OS         string         `json:"os,omitempty"`
	Arch       string         `json:"arch,omitempty"`
}

type WhichRequest struct {
	Paths []string `json:"paths,omitempty"`
	Names []string `json:"names,omitempty"`
}

type WhichHit struct {
	Query    string `json:"query"`
	Path     string `json:"path"`
	Size     int64  `json:"size,omitempty"`
	MtimeUTC string `json:"mtimeUtc,omitempty"`
}

type WhichResult struct {
	Hits []WhichHit `json:"hits"`
}

type ReadRequest struct {
	Paths []string `json:"paths"`
	Limit int      `json:"limit,omitempty"`
}

type ReadFile struct {
	Query     string `json:"query"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	MtimeUTC  string `json:"mtimeUtc,omitempty"`
	Data      []byte `json:"data"`
	Truncated bool   `json:"truncated,omitempty"`
}

type ReadResult struct {
	Files []ReadFile `json:"files"`
}

type SpawnRequest struct {
	Path string            `json:"path"`
	Args []string          `json:"args,omitempty"`
	Cwd  string            `json:"cwd,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

type SpawnResult struct {
	PID  int      `json:"pid"`
	Path string   `json:"path"`
	Args []string `json:"args,omitempty"`
}

type ListenersRequest struct {
	Names []string `json:"names,omitempty"`
}

type Listener struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
	PID  int32  `json:"pid,omitempty"`
	Name string `json:"name,omitempty"`
}

type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func errf(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

func ErrBad(format string, args ...any) *Error {
	return errf(http.StatusBadRequest, "bad_request", format, args...)
}

func ErrOffline() *Error {
	return errf(http.StatusConflict, "guest_offline", "guest agent not connected")
}

func RequireConfirm(confirm bool) error {
	if confirm {
		return nil
	}
	return errf(http.StatusForbidden, "confirm_required", "destructive action requires confirm=true or header X-Confirm: true")
}

type Host interface {
	Agents() []SessionInfo
	Agent(id string) (SessionInfo, bool)
	DialTCP(ctx context.Context, id, target string) (net.Conn, error)
	HTTPClient(id, target string) *http.Client
	Call(ctx context.Context, id, primitive string, params any) (json.RawMessage, error)
	Audit(r *http.Request, id, kind, detail string, err error)
}

// Plugin is implemented by a plugin process. Identity lives in manifest.json,
// which the hub passes as VARTIO_PLUGIN_MANIFEST.
type Plugin interface {
	Init(ctx context.Context, host Host) error
	Routes(mux *http.ServeMux, prefix string)
}

type UIPlugin interface {
	UI() fs.FS
}

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

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, err error) {
	var api *Error
	if !errors.As(err, &api) {
		api = &Error{Status: http.StatusBadGateway, Code: "error", Message: err.Error()}
	}
	WriteJSON(w, api.Status, map[string]any{"code": api.Code, "message": api.Message})
}

type client struct {
	base  string
	token string
	httpc *http.Client
}

func (c *client) do(ctx context.Context, method, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var api Error
		_ = json.NewDecoder(resp.Body).Decode(&api)
		if api.Status == 0 {
			api.Status = resp.StatusCode
		}
		if api.Message == "" {
			api.Message = resp.Status
		}
		return &api
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *client) Agents() []SessionInfo {
	var out []SessionInfo
	if err := c.do(context.Background(), http.MethodGet, "/agents", nil, &out); err != nil || out == nil {
		return []SessionInfo{}
	}
	return out
}

func (c *client) Agent(id string) (SessionInfo, bool) {
	var info SessionInfo
	if err := c.do(context.Background(), http.MethodGet, "/agents/"+id, nil, &info); err != nil {
		return SessionInfo{}, false
	}
	return info, true
}

func (c *client) DialTCP(ctx context.Context, id, target string) (net.Conn, error) {
	var resp struct {
		Addr string `json:"addr"`
	}
	if err := c.do(ctx, http.MethodPost, "/dial", map[string]string{"agent": id, "target": target}, &resp); err != nil {
		return nil, err
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", resp.Addr)
}

func (c *client) HTTPClient(id, target string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return c.DialTCP(ctx, id, target)
		},
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 2 * time.Minute,
	}, Timeout: 3 * time.Minute}
}

func (c *client) Call(ctx context.Context, id, primitive string, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var out json.RawMessage
	err = c.do(ctx, http.MethodPost, "/call", map[string]any{"agent": id, "primitive": primitive, "params": json.RawMessage(raw)}, &out)
	return out, err
}

func (c *client) Audit(r *http.Request, id, kind, detail string, err error) {
	body := map[string]string{"agent": id, "kind": kind, "detail": detail}
	if err != nil {
		body["error"] = err.Error()
	}
	if r != nil {
		host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
		if splitErr != nil {
			host = r.RemoteAddr
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			host = xff
		}
		body["remote"] = host
	}
	_ = c.do(context.Background(), http.MethodPost, "/audit", body, nil)
}

type helloMsg struct {
	Listen   string   `json:"listen"`
	UI       bool     `json:"ui"`
	Manifest Manifest `json:"manifest"`
}

// Serve reads VARTIO_PLUGIN_MANIFEST and listens on loopback. The hub address
// and token come from VARTIO_PLUGIN_HUB and VARTIO_PLUGIN_TOKEN. One JSON line
// on stdout tells the hub where to proxy.
func Serve(p Plugin) error {
	base := os.Getenv("VARTIO_PLUGIN_HUB")
	token := os.Getenv("VARTIO_PLUGIN_TOKEN")
	path := os.Getenv("VARTIO_PLUGIN_MANIFEST")
	if base == "" || token == "" || path == "" {
		return fmt.Errorf("VARTIO_PLUGIN_HUB, VARTIO_PLUGIN_TOKEN and VARTIO_PLUGIN_MANIFEST are required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	m, err := ParseManifest(raw)
	if err != nil {
		return err
	}
	host := &client{base: base, token: token, httpc: &http.Client{Timeout: 3 * time.Minute}}
	if err := p.Init(context.Background(), host); err != nil {
		return err
	}
	mux := http.NewServeMux()
	p.Routes(mux, PathPrefix+"/"+m.Name)
	if u, ok := p.(UIPlugin); ok {
		if !m.UI {
			return fmt.Errorf("plugin %s has a UI but manifest ui is false", m.Name)
		}
		mux.Handle("GET "+UIPrefix+"/"+m.Name+"/", http.StripPrefix(UIPrefix+"/"+m.Name+"/", http.FileServerFS(u.UI())))
	} else if m.UI {
		return fmt.Errorf("plugin %s declares ui but does not provide one", m.Name)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	b, err := json.Marshal(helloMsg{Listen: ln.Addr().String(), UI: m.UI, Manifest: m})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(os.Stdout, "%s\n", b); err != nil {
		return err
	}
	return http.Serve(ln, mux)
}
