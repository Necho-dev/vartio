package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"vartio/internal/common"
	"vartio/internal/entities"
	"vartio/internal/hub"
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
	h := &scopedHost{hub: hub.NewHub(""), m: Manifest{Name: "x", Primitives: []string{entities.CapTunnelTCP}, Targets: []string{"127.0.0.1:1"}}}
	_, err := h.DialTCP(context.Background(), "a", "127.0.0.1:2")
	var api *common.Error
	if !errors.As(err, &api) || api.Code != "plugin_forbidden" {
		t.Fatalf("undeclared target: %v", err)
	}
	h.m.Primitives = nil
	if _, err := h.DialTCP(context.Background(), "a", "127.0.0.1:1"); !errors.As(err, &api) || api.Code != "plugin_forbidden" {
		t.Fatalf("undeclared primitive: %v", err)
	}
}

func get(t *testing.T, mux http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

type copyBin struct{ bin string }

func (c copyBin) Fetch(_ context.Context, _ CatalogItem, dest string) (string, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(dest, "vartio-plugin")
	b, err := os.ReadFile(c.bin)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(out, b, 0o755); err != nil {
		return "", err
	}
	return out, nil
}

func TestRegistryDownloadsAndStarts(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "hello")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "testdata/hello"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build hello plugin: %v\n%s", err, out)
	}

	r := NewRegistry(hub.NewHub(""))
	r.Dir = filepath.Join(dir, "plugins")
	r.fetch = copyBin{bin}
	r.catalog = []CatalogItem{{
		Name: "hello", Git: "unused",
		Manifest: Manifest{Name: "hello", Title: "Hello", Version: "0.0.1", Primitives: []string{entities.CapTunnelTCP}},
	}}
	state := filepath.Join(dir, "plugins.json")
	if err := r.Load(state, nil); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", http.NotFound)
	r.Mount(mux)

	var before []listItem
	if err := json.Unmarshal(get(t, mux, http.MethodGet, PathPrefix).Body.Bytes(), &before); err != nil || len(before) != 1 || before[0].Installed || before[0].Name != "hello" {
		t.Fatalf("catalog: %+v", before)
	}
	if rec := get(t, mux, http.MethodGet, PathPrefix+"/hello/ping"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "plugin_not_installed") {
		t.Fatalf("before install: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get(t, mux, http.MethodPost, PathPrefix+"/hello"); rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(state); !strings.Contains(string(b), `"hello"`) {
		t.Fatalf("state not saved: %s", b)
	}
	for path, want := range map[string]string{
		PathPrefix + "/hello/ping":   "pong",
		UIPrefix + "/hello/index.js": "export function mount() {}",
	} {
		if rec := get(t, mux, http.MethodGet, path); rec.Body.String() != want {
			t.Fatalf("%s: %d %q", path, rec.Code, rec.Body.String())
		}
	}
	if rec := get(t, mux, http.MethodPost, PathPrefix+"/zzz"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown plugin: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get(t, mux, http.MethodDelete, PathPrefix+"/hello"); rec.Code != http.StatusOK {
		t.Fatalf("uninstall: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get(t, mux, http.MethodGet, UIPrefix+"/hello/index.js"); rec.Code != http.StatusNotFound {
		t.Fatalf("UI after uninstall: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "hello")); !os.IsNotExist(err) {
		t.Fatalf("plugin dir still there: %v", err)
	}

	r2 := NewRegistry(hub.NewHub(""))
	if err := r2.Load(state, []string{"hello"}); err != nil {
		t.Fatal(err)
	}
	r2.mu.Lock()
	wanted := append([]string(nil), r2.wanted...)
	r2.mu.Unlock()
	if len(wanted) != 0 {
		t.Fatalf("saved uninstall ignored: %v", wanted)
	}
}

func TestScopedHostCall(t *testing.T) {
	h := &scopedHost{hub: hub.NewHub(""), m: Manifest{Name: "x", Primitives: []string{entities.CapFsWhich}}}
	var api *common.Error
	if _, err := h.Call(context.Background(), "a", entities.CapProcSpawn, nil); !errors.As(err, &api) || api.Code != "plugin_forbidden" {
		t.Fatalf("undeclared primitive: %v", err)
	}
	if _, err := h.Call(context.Background(), "a", entities.CapFsWhich, nil); !errors.As(err, &api) || api.Code != "guest_offline" {
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
	m := Manifest{Primitives: []string{entities.CapTunnelTCP}}
	if a := readiness(m, entities.SessionInfo{DeviceID: "d", Primitives: map[string]int{entities.CapTunnelTCP: 1}}); !a.Ready {
		t.Fatalf("%+v", a)
	}
	if a := readiness(m, entities.SessionInfo{DeviceID: "d"}); a.Ready || len(a.Missing) != 1 {
		t.Fatalf("%+v", a)
	}
}
