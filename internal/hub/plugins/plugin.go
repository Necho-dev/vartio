// Package plugin hosts hub-side extensions. Plugins never talk to an agent
// directly: they get a Host scoped to what their Manifest declares, built on
// the agent's general-purpose primitives (guest.PrimitiveVersions).
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"vartio/internal/common"
	"vartio/internal/entities"
	"vartio/internal/hub"
)

// PathPrefix sits under /v1/probe so probe-hub's admin token and vm-server's
// API token both cover plugin routes. PublicPrefix is under /v1/probe/ws/,
// which skips that token: plugins authenticate it themselves (Tickets), for
// clients such as browser WebSockets that cannot send headers. UIPrefix
// serves each plugin's front-end module, which the hub page mounts.
const (
	PathPrefix   = entities.PathPrefix + "/plugins"
	PublicPrefix = entities.PathPrefix + "/ws/plugins"
	UIPrefix     = "/plugins"
)

type Manifest struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	// Primitives are the agent caps the plugin needs (guest.CapTunnelTCP, ...).
	Primitives []string `json:"primitives"`
	// Targets are the host:port values the plugin may dial on an agent;
	// "host:*" allows any port on host.
	Targets []string `json:"targets,omitempty"`
}

type Plugin interface {
	Manifest() Manifest
	Init(ctx context.Context, host Host) error
	// Routes mounts handlers under prefix (PathPrefix + "/" + name) and,
	// optionally, PublicPrefix + "/" + name. Requests reach them only while
	// the plugin is installed.
	Routes(mux *http.ServeMux, prefix string)
}

// UIPlugin ships a front-end module: UI() must hold index.js exporting
// mount(el, ctx), served at UIPrefix/{name}/.
type UIPlugin interface {
	UI() fs.FS
}

// Stopper is called on uninstall, after the plugin's tunnels are closed.
type Stopper interface {
	Stop()
}

// Host is everything a plugin may do with agents.
type Host interface {
	Agents() []entities.SessionInfo
	Agent(id string) (entities.SessionInfo, bool)
	// DialTCP opens a tunnel from the agent to target.
	DialTCP(ctx context.Context, id, target string) (net.Conn, error)
	// HTTPClient sends requests to target on the agent's machine; the URL host is ignored.
	HTTPClient(id, target string) *http.Client
	// Call invokes an RPC primitive (guest.PrimitiveMethods) the manifest declares.
	Call(ctx context.Context, id, primitive string, params any) (json.RawMessage, error)
	// Audit records an action on agent id as plugin.{name}.{kind}.
	Audit(r *http.Request, id, kind, detail string, err error)
}

type Registry struct {
	hub *hub.Hub

	mu        sync.Mutex
	plugins   []*entry
	statePath string
}

type entry struct {
	p         Plugin
	m         Manifest
	host      *scopedHost
	mux       *http.ServeMux
	installed bool
}

func NewRegistry(hub *hub.Hub) *Registry {
	return &Registry{hub: hub}
}

// Add registers p as installed; Load then applies the persisted state.
func (r *Registry) Add(ctx context.Context, p Plugin) error {
	m := p.Manifest()
	if m.Name == "" || strings.ContainsAny(m.Name, "/ ") {
		return fmt.Errorf("plugin name %q is invalid", m.Name)
	}
	if r.find(m.Name) != nil {
		return fmt.Errorf("plugin %s registered twice", m.Name)
	}
	for _, c := range m.Primitives {
		if _, ok := entities.PrimitiveVersions[c]; !ok {
			return fmt.Errorf("plugin %s needs unknown primitive %s", m.Name, c)
		}
	}
	h := &scopedHost{hub: r.hub, m: m}
	if err := p.Init(ctx, h); err != nil {
		return fmt.Errorf("plugin %s: %w", m.Name, err)
	}
	e := &entry{p: p, m: m, host: h, mux: http.NewServeMux(), installed: true}
	p.Routes(e.mux, PathPrefix+"/"+m.Name)
	r.mu.Lock()
	r.plugins = append(r.plugins, e)
	r.mu.Unlock()
	return nil
}

type stateFile struct {
	Installed []string `json:"installed"`
}

// Load makes path the install-state file. Without the file, only defaults
// are installed.
func (r *Registry) Load(path string, defaults []string) error {
	installed := defaults
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		var st stateFile
		if err := json.Unmarshal(b, &st); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		installed = st.Installed
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statePath = path
	for _, e := range r.plugins {
		e.installed = slices.Contains(installed, e.m.Name)
	}
	return nil
}

func (r *Registry) find(name string) *entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.plugins {
		if e.m.Name == name {
			return e
		}
	}
	return nil
}

// SetInstalled installs or uninstalls a plugin. Uninstalling closes its
// tunnels and stops it; routes answer 404 plugin_not_installed afterwards.
func (r *Registry) SetInstalled(name string, on bool) error {
	r.mu.Lock()
	var e *entry
	for _, x := range r.plugins {
		if x.m.Name == name {
			e = x
		}
	}
	if e == nil {
		r.mu.Unlock()
		return errNotFound(name)
	}
	changed := e.installed != on
	e.installed = on
	err := r.saveLocked()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if changed && !on {
		r.hub.CloseTunnels("plugin:" + name)
		if s, ok := e.p.(Stopper); ok {
			s.Stop()
		}
	}
	if changed {
		log.Printf("plugin %s installed=%v", name, on)
	}
	return nil
}

func (r *Registry) saveLocked() error {
	if r.statePath == "" {
		return nil
	}
	st := stateFile{Installed: []string{}}
	for _, e := range r.plugins {
		if e.installed {
			st.Installed = append(st.Installed, e.m.Name)
		}
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	if dir := filepath.Dir(r.statePath); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := r.statePath + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.statePath)
}

func (r *Registry) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET "+PathPrefix, r.handleList)
	mux.HandleFunc("GET "+PathPrefix+"/{name}", r.handleGet)
	mux.HandleFunc("POST "+PathPrefix+"/{name}", r.handleInstall(true))
	mux.HandleFunc("DELETE "+PathPrefix+"/{name}", r.handleInstall(false))
	// Per method: a methodless pattern would conflict with a host's "GET /".
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		mux.HandleFunc(m+" "+PathPrefix+"/{name}/", r.dispatch)
		mux.HandleFunc(m+" "+PublicPrefix+"/{name}/", r.dispatch)
	}
	mux.HandleFunc("GET "+UIPrefix+"/{name}/{file...}", r.handleUI)
}

func (r *Registry) live(w http.ResponseWriter, name string) *entry {
	e := r.find(name)
	if e == nil {
		WriteError(w, errNotFound(name))
		return nil
	}
	r.mu.Lock()
	on := e.installed
	r.mu.Unlock()
	if !on {
		WriteError(w, &common.Error{Status: http.StatusNotFound, Code: "plugin_not_installed", Message: "plugin " + name + " is not installed"})
		return nil
	}
	return e
}

func (r *Registry) dispatch(w http.ResponseWriter, req *http.Request) {
	if e := r.live(w, req.PathValue("name")); e != nil {
		e.mux.ServeHTTP(w, req)
	}
}

func (r *Registry) handleUI(w http.ResponseWriter, req *http.Request) {
	e := r.live(w, req.PathValue("name"))
	if e == nil {
		return
	}
	u, ok := e.p.(UIPlugin)
	if f := req.PathValue("file"); !ok || f == "" || strings.HasSuffix(f, "/") {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.StripPrefix(UIPrefix+"/"+e.m.Name+"/", http.FileServerFS(u.UI())).ServeHTTP(w, req)
}

// AgentReadiness says whether one online agent has every primitive a plugin needs.
type AgentReadiness struct {
	ID       string   `json:"id"`
	HostName string   `json:"hostname,omitempty"`
	OS       string   `json:"os,omitempty"`
	Version  string   `json:"version,omitempty"`
	Ready    bool     `json:"ready"`
	Missing  []string `json:"missing,omitempty"`
}

type listItem struct {
	Manifest
	Installed bool `json:"installed"`
	// UI is the front-end module URL; empty when the plugin has none.
	UI     string           `json:"ui,omitempty"`
	Agents []AgentReadiness `json:"agents"`
}

func (r *Registry) item(e *entry, sessions []entities.SessionInfo) listItem {
	r.mu.Lock()
	it := listItem{Manifest: e.m, Installed: e.installed, Agents: []AgentReadiness{}}
	r.mu.Unlock()
	if _, ok := e.p.(UIPlugin); ok {
		it.UI = UIPrefix + "/" + e.m.Name + "/index.js"
	}
	for _, s := range sessions {
		it.Agents = append(it.Agents, readiness(e.m, s))
	}
	return it
}

func (r *Registry) sessions() []entities.SessionInfo {
	s := r.hub.List()
	sort.Slice(s, func(i, j int) bool { return s[i].HostName < s[j].HostName })
	return s
}

func (r *Registry) handleList(w http.ResponseWriter, _ *http.Request) {
	sessions := r.sessions()
	r.mu.Lock()
	all := slices.Clone(r.plugins)
	r.mu.Unlock()
	out := make([]listItem, 0, len(all))
	for _, e := range all {
		out = append(out, r.item(e, sessions))
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Registry) handleGet(w http.ResponseWriter, req *http.Request) {
	e := r.find(req.PathValue("name"))
	if e == nil {
		WriteError(w, errNotFound(req.PathValue("name")))
		return
	}
	writeJSON(w, http.StatusOK, r.item(e, r.sessions()))
}

func (r *Registry) handleInstall(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		name := req.PathValue("name")
		if err := r.SetInstalled(name, on); err != nil {
			WriteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, r.item(r.find(name), r.sessions()))
	}
}

func readiness(m Manifest, s entities.SessionInfo) AgentReadiness {
	a := AgentReadiness{ID: s.DeviceID, HostName: s.HostName, OS: s.OS, Version: s.Version}
	if a.ID == "" {
		a.ID = s.BiosUUID
	}
	for _, c := range m.Primitives {
		if s.Primitives[c] < entities.PrimitiveVersions[c] {
			a.Missing = append(a.Missing, c)
		}
	}
	a.Ready = len(a.Missing) == 0
	return a
}

// scopedHost enforces the plugin's manifest on every agent operation.
type scopedHost struct {
	hub *hub.Hub
	m   Manifest
}

func (h *scopedHost) Agents() []entities.SessionInfo { return h.hub.List() }

func (h *scopedHost) Agent(id string) (entities.SessionInfo, bool) { return h.hub.Info(id) }

func (h *scopedHost) DialTCP(ctx context.Context, id, target string) (net.Conn, error) {
	if !slices.Contains(h.m.Primitives, entities.CapTunnelTCP) {
		return nil, errForbidden(h.m.Name + " did not declare " + entities.CapTunnelTCP)
	}
	if !targetAllowed(h.m.Targets, target) {
		return nil, errForbidden(h.m.Name + " may not dial " + target)
	}
	return h.hub.DialTCP(ctx, id, target, "plugin:"+h.m.Name)
}

func (h *scopedHost) HTTPClient(id, target string) *http.Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return h.DialTCP(ctx, id, target)
		},
		// One tunnel per request: idle keep-alive tunnels would count against
		// the agent's tunnel cap until they time out.
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 2 * time.Minute,
	}
	return &http.Client{Transport: tr, Timeout: 3 * time.Minute}
}

func (h *scopedHost) Call(ctx context.Context, id, primitive string, params any) (json.RawMessage, error) {
	if !slices.Contains(h.m.Primitives, primitive) {
		return nil, errForbidden(h.m.Name + " did not declare " + primitive)
	}
	return h.hub.CallPrimitive(ctx, id, primitive, params)
}

func (h *scopedHost) Audit(r *http.Request, id, kind, detail string, err error) {
	h.hub.Record(r, id, "plugin."+h.m.Name+"."+kind, detail, err)
}

func targetAllowed(allowed []string, target string) bool {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return false
	}
	for _, a := range allowed {
		ah, ap, err := net.SplitHostPort(a)
		if err != nil || !strings.EqualFold(ah, host) {
			continue
		}
		if ap == "*" || ap == port {
			return true
		}
	}
	return false
}

func errForbidden(msg string) error {
	return &common.Error{Status: http.StatusForbidden, Code: "plugin_forbidden", Message: msg}
}

func errNotFound(name string) error {
	return &common.Error{Status: http.StatusNotFound, Code: "plugin_not_found", Message: "no plugin named " + name}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteJSON writes v with the plugin API's JSON headers.
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }

// WriteError writes err in the shared {code, message} shape.
func WriteError(w http.ResponseWriter, err error) {
	var api *common.Error
	if !errors.As(err, &api) {
		api = &common.Error{Status: http.StatusBadGateway, Code: "error", Message: err.Error()}
	}
	writeJSON(w, api.Status, map[string]any{"code": api.Code, "message": api.Message})
}
