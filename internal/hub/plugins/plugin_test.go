package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"govmomi-wmks/internal/esxi"
	"govmomi-wmks/internal/guest"
)

func TestTargetAllowed(t *testing.T) {
	allowed := []string{"127.0.0.1:54345", "localhost:*"}
	for target, want := range map[string]bool{
		"127.0.0.1:54345": true,
		"127.0.0.1:9222":  false,
		"localhost:9222":  true,
		"10.0.0.1:54345":  false,
		"garbage":         false,
	} {
		if got := targetAllowed(allowed, target); got != want {
			t.Errorf("%s: got %v want %v", target, got, want)
		}
	}
}

func TestScopedHostEnforcesManifest(t *testing.T) {
	h := &scopedHost{hub: guest.NewHub(""), m: Manifest{Name: "x", Primitives: []string{guest.CapTunnelTCP}, Targets: []string{"127.0.0.1:1"}}}
	_, err := h.DialTCP(context.Background(), "a", "127.0.0.1:2")
	var api *esxi.Error
	if !errors.As(err, &api) || api.Code != "plugin_forbidden" {
		t.Fatalf("undeclared target: %v", err)
	}
	h.m.Primitives = nil
	if _, err := h.DialTCP(context.Background(), "a", "127.0.0.1:1"); !errors.As(err, &api) || api.Code != "plugin_forbidden" {
		t.Fatalf("undeclared primitive: %v", err)
	}
}

type stub struct {
	m       Manifest
	stopped *bool
}

func (s stub) Manifest() Manifest               { return s.m }
func (s stub) Init(context.Context, Host) error { return nil }
func (s stub) Routes(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("GET "+prefix+"/ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("pong")) })
	mux.HandleFunc("GET "+PublicPrefix+"/"+s.m.Name+"/open", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("public")) })
}
func (s stub) UI() fs.FS {
	return fstest.MapFS{"index.js": {Data: []byte("export function mount() {}")}}
}
func (s stub) Stop() {
	if s.stopped != nil {
		*s.stopped = true
	}
}

func get(t *testing.T, mux http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestRegistry(t *testing.T) {
	r := NewRegistry(guest.NewHub(""))
	stopped := false
	if err := r.Add(context.Background(), stub{m: Manifest{Name: "a", Primitives: []string{guest.CapTunnelTCP}}, stopped: &stopped}); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(context.Background(), stub{m: Manifest{Name: "a"}}); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if err := r.Add(context.Background(), stub{m: Manifest{Name: "b", Primitives: []string{"warp.drive"}}}); err == nil {
		t.Fatal("unknown primitive accepted")
	}
	state := filepath.Join(t.TempDir(), "plugins.json")
	if err := r.Load(state, nil); err != nil {
		t.Fatal(err)
	}
	// Both host page styles: probe-hub's "GET /" and vm-server's "/".
	for _, root := range []string{"GET /", "/"} {
		mux := http.NewServeMux()
		mux.HandleFunc(root, http.NotFound)
		r.Mount(mux)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", http.NotFound)
	r.Mount(mux)

	if rec := get(t, mux, http.MethodGet, PathPrefix+"/a/ping"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "plugin_not_installed") {
		t.Fatalf("uninstalled route: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get(t, mux, http.MethodPost, PathPrefix+"/a"); rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(state); !strings.Contains(string(b), `"a"`) {
		t.Fatalf("state not saved: %s", b)
	}
	for path, want := range map[string]string{PathPrefix + "/a/ping": "pong", PublicPrefix + "/a/open": "public", UIPrefix + "/a/index.js": "export function mount() {}"} {
		if rec := get(t, mux, http.MethodGet, path); rec.Body.String() != want {
			t.Fatalf("%s: %d %q", path, rec.Code, rec.Body.String())
		}
	}
	var list []listItem
	if err := json.Unmarshal(get(t, mux, http.MethodGet, PathPrefix).Body.Bytes(), &list); err != nil || len(list) != 1 || !list[0].Installed || list[0].UI != UIPrefix+"/a/index.js" {
		t.Fatalf("list %+v", list)
	}
	if rec := get(t, mux, http.MethodPost, PathPrefix+"/zzz"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown plugin: %d", rec.Code)
	}

	if rec := get(t, mux, http.MethodDelete, PathPrefix+"/a"); rec.Code != http.StatusOK || !stopped {
		t.Fatalf("uninstall: %d stopped=%v", rec.Code, stopped)
	}
	if rec := get(t, mux, http.MethodGet, UIPrefix+"/a/index.js"); rec.Code != http.StatusNotFound {
		t.Fatalf("UI of uninstalled plugin: %d", rec.Code)
	}

	// A fresh registry picks the saved state up.
	r2 := NewRegistry(guest.NewHub(""))
	_ = r2.Add(context.Background(), stub{m: Manifest{Name: "a"}})
	if err := r2.Load(state, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if r2.find("a").installed {
		t.Fatal("saved uninstall ignored in favour of defaults")
	}
}

func TestScopedHostCall(t *testing.T) {
	h := &scopedHost{hub: guest.NewHub(""), m: Manifest{Name: "x", Primitives: []string{guest.CapFsWhich}}}
	var api *esxi.Error
	if _, err := h.Call(context.Background(), "a", guest.CapProcSpawn, nil); !errors.As(err, &api) || api.Code != "plugin_forbidden" {
		t.Fatalf("undeclared primitive: %v", err)
	}
	if _, err := h.Call(context.Background(), "a", guest.CapFsWhich, nil); !errors.As(err, &api) || api.Code != "guest_offline" {
		t.Fatalf("offline agent: %v", err)
	}
}

func TestTickets(t *testing.T) {
	var tk Tickets[string]
	id, _ := tk.Issue("v")
	if v, ok := tk.Take(id); !ok || v != "v" {
		t.Fatal("ticket not redeemed")
	}
	if _, ok := tk.Take(id); ok {
		t.Fatal("ticket redeemed twice")
	}
}

func TestReadiness(t *testing.T) {
	m := Manifest{Primitives: []string{guest.CapTunnelTCP}}
	if a := readiness(m, guest.SessionInfo{DeviceID: "d", Primitives: map[string]int{guest.CapTunnelTCP: 1}}); !a.Ready {
		t.Fatalf("%+v", a)
	}
	if a := readiness(m, guest.SessionInfo{DeviceID: "d"}); a.Ready || len(a.Missing) != 1 {
		t.Fatalf("%+v", a)
	}
}
