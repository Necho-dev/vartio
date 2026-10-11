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
	"net"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/Necho-dev/vartio/sdk"
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

// Manifest is the plugin contract. Identity comes from the signed package,
// not from a method on the process.
type Manifest = sdk.Manifest

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

// Registry installs plugins by downloading signed packages and running each
// one as its own process. A name in the state file is installed.
type Registry struct {
	hub *hub.Hub
	Dir string

	mu           sync.Mutex
	catalog      []CatalogItem
	statePath    string
	wanted       []string
	manual       []manualEntry
	repos        []repoEntry
	pins         []pinEntry
	keys         map[string][]byte
	remote       []CatalogItem
	repoProblems map[string]string
	running      map[string]*proc
	problems     map[string]string
	starting     map[string]bool
}

func NewRegistry(h *hub.Hub) *Registry {
	r := &Registry{
		hub:          h,
		Dir:          "vartio-plugins",
		catalog:      DefaultCatalog(),
		running:      map[string]*proc{},
		problems:     map[string]string{},
		starting:     map[string]bool{},
		repoProblems: map[string]string{},
		keys:         map[string][]byte{},
	}
	r.keys[officialKeyID] = append([]byte(nil), officialPublic()...)
	return r
}

type stateFile struct {
	Installed []string      `json:"installed"`
	Manual    []manualEntry `json:"manual,omitempty"`
	Repos     []repoEntry   `json:"repos,omitempty"`
	Pins      []pinEntry    `json:"pins,omitempty"`
}

// Load reads which plugins are installed. Without the file, defaults is that
// list (VARTIO_PLUGINS). It does not start them; call Restore.
func (r *Registry) Load(path string, defaults []string) error {
	installed := defaults
	var st stateFile
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &st); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		installed = st.Installed
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	r.mu.Lock()
	r.statePath = path
	r.wanted = append([]string(nil), installed...)
	r.repos = r.keepRepos(cleanRepos(st.Repos))
	r.manual = r.keepManual(cleanManual(st.Manual))
	r.pins = cleanPins(st.Pins)
	r.mu.Unlock()
	return nil
}

func cleanManual(in []manualEntry) []manualEntry {
	out := make([]manualEntry, 0, len(in))
	seen := map[string]bool{}
	for _, e := range in {
		e.Name = strings.TrimSpace(e.Name)
		e.URL = strings.TrimSpace(e.URL)
		e.SigningKeyID = strings.TrimSpace(e.SigningKeyID)
		sum, err := normalizeSHA(e.SHA256)
		if err != nil || seen[e.Name] || !pluginName.MatchString(e.Name) || e.SigningKeyID == "" {
			continue
		}
		if _, err := validateFetchURL(e.URL); err != nil {
			continue
		}
		e.SHA256 = sum
		seen[e.Name] = true
		out = append(out, e)
	}
	return out
}

func (r *Registry) keepManual(in []manualEntry) []manualEntry {
	out := make([]manualEntry, 0, len(in))
	for _, e := range in {
		if _, ok := r.keys[e.SigningKeyID]; ok {
			out = append(out, e)
		}
	}
	return out
}

func cleanPins(in []pinEntry) []pinEntry {
	out := make([]pinEntry, 0, len(in))
	seen := map[string]bool{}
	for _, e := range in {
		e.Name = strings.TrimSpace(e.Name)
		sum, err := normalizeSHA(e.SHA256)
		if err != nil || seen[e.Name] || !pluginName.MatchString(e.Name) {
			continue
		}
		e.SHA256 = sum
		seen[e.Name] = true
		out = append(out, e)
	}
	return out
}

func cleanRepos(in []repoEntry) []repoEntry {
	out := make([]repoEntry, 0, len(in))
	seen := map[string]bool{}
	for _, e := range in {
		e.ID = strings.TrimSpace(e.ID)
		e.Name = strings.TrimSpace(e.Name)
		e.URL = strings.TrimSpace(e.URL)
		e.KeyID = strings.TrimSpace(e.KeyID)
		e.PublicKey = strings.TrimSpace(e.PublicKey)
		if seen[e.ID] || !pluginName.MatchString(e.ID) || e.ID == "vartio" || e.Name == "" || e.KeyID == "" {
			continue
		}
		if _, err := validateFetchURL(e.URL); err != nil {
			continue
		}
		if _, err := sdk.ParsePublicKey(e.PublicKey); err != nil {
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	return out
}

func (r *Registry) keepRepos(in []repoEntry) []repoEntry {
	out := make([]repoEntry, 0, len(in))
	for _, e := range in {
		pub, err := sdk.ParsePublicKey(e.PublicKey)
		if err != nil {
			continue
		}
		if err := r.trust(e.KeyID, pub); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (r *Registry) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET "+PathPrefix, r.handleList)
	mux.HandleFunc("GET "+PathPrefix+"/_sources", r.handleSources)
	mux.HandleFunc("POST "+PathPrefix+"/_sources", r.handleAddRepo)
	mux.HandleFunc("DELETE "+PathPrefix+"/_sources/{id}", r.handleDelRepo)
	mux.HandleFunc("POST "+PathPrefix+"/_manual", r.handleAddManual)
	mux.HandleFunc("DELETE "+PathPrefix+"/_manual/{name}", r.handleDelManual)
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

func (r *Registry) dispatch(w http.ResponseWriter, req *http.Request) {
	p := r.ready(w, req.PathValue("name"))
	if p != nil {
		p.proxy.ServeHTTP(w, req)
	}
}

func (r *Registry) handleUI(w http.ResponseWriter, req *http.Request) {
	p := r.ready(w, req.PathValue("name"))
	if p == nil {
		return
	}
	if !p.ui {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	p.proxy.ServeHTTP(w, req)
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
	// Status is ready, installing, or error. Empty means not installed.
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
	// UI is the front-end module URL; empty when the plugin has none or is not ready.
	UI string `json:"ui,omitempty"`
	// Repo is the plugin's source repository as an https URL.
	Repo string `json:"repo,omitempty"`
	// Official is set for plugins shipped with the hub.
	Official bool `json:"official,omitempty"`
	// Origin is the catalog label: Vartio 官方, a custom repository name, or 手动安装.
	Origin string           `json:"origin,omitempty"`
	Agents []AgentReadiness `json:"agents"`
}

func (r *Registry) item(name string, sessions []entities.SessionInfo) (listItem, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cat, known := r.catalogBy(name)
	p := r.running[name]
	wanted := slices.Contains(r.wanted, name)
	if !known && p == nil && !wanted {
		return listItem{}, false
	}
	m := cat.Manifest
	if m.Name == "" {
		m.Name = name
	}
	it := listItem{Manifest: m, Installed: wanted, Repo: repoWeb(m.Source), Official: cat.Official, Origin: cat.Origin, Agents: []AgentReadiness{}}
	switch {
	case p != nil:
		it.Status = "ready"
		it.Manifest = p.manifest
		if src := repoWeb(p.manifest.Source); src != "" {
			it.Repo = src
		}
		if p.ui {
			it.UI = UIPrefix + "/" + name + "/index.js"
		}
	case r.starting[name]:
		it.Status = "installing"
	case r.problems[name] != "":
		it.Status = "error"
		it.Error = r.problems[name]
	}
	for _, s := range sessions {
		it.Agents = append(it.Agents, readiness(it.Manifest, s))
	}
	return it, true
}

func repoWeb(source string) string {
	source = strings.TrimSuffix(strings.TrimSpace(source), ".git")
	if strings.HasPrefix(source, "https://github.com/") {
		return source
	}
	return ""
}

func (r *Registry) sessions() []entities.SessionInfo {
	s := r.hub.List()
	sort.Slice(s, func(i, j int) bool { return s[i].HostName < s[j].HostName })
	return s
}

func (r *Registry) handleList(w http.ResponseWriter, _ *http.Request) {
	sessions := r.sessions()
	r.mu.Lock()
	names := map[string]bool{}
	for _, c := range r.catalog {
		names[c.Name] = true
	}
	for _, c := range r.manual {
		names[c.Name] = true
	}
	for _, c := range r.remote {
		names[c.Name] = true
	}
	for _, c := range r.pins {
		names[c.Name] = true
	}
	for _, n := range r.wanted {
		names[n] = true
	}
	r.mu.Unlock()
	order := make([]string, 0, len(names))
	for n := range names {
		order = append(order, n)
	}
	sort.Strings(order)
	out := make([]listItem, 0, len(order))
	for _, n := range order {
		if it, ok := r.item(n, sessions); ok {
			out = append(out, it)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Registry) handleGet(w http.ResponseWriter, req *http.Request) {
	it, ok := r.item(req.PathValue("name"), r.sessions())
	if !ok {
		WriteError(w, errNotFound(req.PathValue("name")))
		return
	}
	writeJSON(w, http.StatusOK, it)
}

func (r *Registry) handleInstall(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		name := req.PathValue("name")
		ctx, cancel := context.WithTimeout(req.Context(), 10*time.Minute)
		defer cancel()
		var err error
		if on {
			err = r.Install(ctx, name)
		} else {
			err = r.Uninstall(name)
		}
		if err != nil {
			WriteError(w, err)
			return
		}
		it, _ := r.item(name, r.sessions())
		writeJSON(w, http.StatusOK, it)
	}
}

func readiness(m Manifest, s entities.SessionInfo) AgentReadiness {
	a := AgentReadiness{ID: s.DeviceID, HostName: s.HostName, OS: s.OS, Version: s.Version}
	if a.ID == "" {
		a.ID = s.BiosUUID
	}
	if !sdk.MachineMatch(m.Machines, s.OS, s.Caps) {
		a.Missing = append(a.Missing, "machine")
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

func (h *scopedHost) Agents() []entities.SessionInfo {
	var out []entities.SessionInfo
	for _, s := range h.hub.List() {
		if sdk.MachineMatch(h.m.Machines, s.OS, s.Caps) {
			out = append(out, s)
		}
	}
	if out == nil {
		out = []entities.SessionInfo{}
	}
	return out
}

func (h *scopedHost) Agent(id string) (entities.SessionInfo, bool) { return h.hub.Info(id) }

func (h *scopedHost) allowAgent(id string) error {
	info, ok := h.hub.Info(id)
	if !ok || !info.Online {
		return common.ErrOffline()
	}
	if !sdk.MachineMatch(h.m.Machines, info.OS, info.Caps) {
		return errForbidden(h.m.Name + " is not for this machine")
	}
	return nil
}

func (h *scopedHost) DialTCP(ctx context.Context, id, target string) (net.Conn, error) {
	if !slices.Contains(h.m.Primitives, entities.CapTunnelTCP) {
		return nil, errForbidden(h.m.Name + " did not declare " + entities.CapTunnelTCP)
	}
	if !targetAllowed(h.m.Targets, target) {
		return nil, errForbidden(h.m.Name + " may not dial " + target)
	}
	if err := h.allowAgent(id); err != nil {
		return nil, err
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
	if err := h.allowAgent(id); err != nil {
		return nil, err
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
