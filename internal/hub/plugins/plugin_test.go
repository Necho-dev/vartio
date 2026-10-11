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
	"runtime"
	"strings"
	"testing"

	sdk "github.com/Necho-dev/vartio/sdk"
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

func TestRegistryDownloadsAndStarts(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "hello")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "testdata/hello"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build hello plugin: %v\n%s", err, out)
	}
	binary, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	const rawManifest = `{
  "manifestVersion": 1,
  "name": "hello",
  "title": "Hello",
  "version": "0.0.1",
  "engines": {"hostApi": "1"},
  "primitives": ["tunnel.tcp"],
  "targets": [],
  "machines": ["mac", "windows", "linux"],
  "ui": true
}`
	manifest, err := sdk.ParseManifest([]byte(rawManifest))
	if err != nil {
		t.Fatal(err)
	}
	key, err := sdk.GenerateKey("test-key")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := sdk.Pack([]byte(rawManifest), binary, key)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(blob)
	}))
	defer srv.Close()

	r := NewRegistry(hub.NewHub(""))
	r.Dir = filepath.Join(dir, "plugins")
	r.keys[key.ID] = key.Public()
	r.catalog = []CatalogItem{{
		Name: "hello", Origin: "test",
		Manifest:  manifest,
		Artifacts: []Artifact{{Target: runtime.GOOS + "-" + runtime.GOARCH, URL: srv.URL, SHA256: packageSHA(blob), Size: int64(len(blob)), SigningKeyID: key.ID}},
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

func TestDistPackagesMatchCatalog(t *testing.T) {
	dir := os.Getenv("VTP_DIST")
	if dir == "" {
		t.Skip()
	}
	pub := officialPublic()
	for _, item := range DefaultCatalog() {
		for _, art := range item.Artifacts {
			name := filepath.Base(art.URL)
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if packageSHA(b) != art.SHA256 || int64(len(b)) != art.Size {
				t.Fatalf("%s hash or size", name)
			}
			pkg, err := sdk.Open(b)
			if err != nil {
				t.Fatal(err)
			}
			got, err := pkg.Verify(pub, art.SigningKeyID)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if !sdk.SameManifest(got, item.Manifest) {
				t.Fatalf("%s manifest mismatch\npackage: %#v\ncatalog: %#v", name, got, item.Manifest)
			}
		}
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
	m := Manifest{Machines: []string{"linux"}, Primitives: []string{entities.CapTunnelTCP}}
	if a := readiness(m, entities.SessionInfo{DeviceID: "d", OS: "linux", Primitives: map[string]int{entities.CapTunnelTCP: 1}}); !a.Ready {
		t.Fatalf("%+v", a)
	}
	if a := readiness(m, entities.SessionInfo{DeviceID: "d", OS: "linux"}); a.Ready || len(a.Missing) != 1 {
		t.Fatalf("%+v", a)
	}
	if a := readiness(m, entities.SessionInfo{DeviceID: "d", OS: "darwin", Primitives: map[string]int{entities.CapTunnelTCP: 1}}); a.Ready || len(a.Missing) != 1 || a.Missing[0] != "machine" {
		t.Fatalf("%+v", a)
	}
	if a := readiness(m, entities.SessionInfo{DeviceID: "d", OS: "linux", Caps: []string{"docker"}, Primitives: map[string]int{entities.CapTunnelTCP: 1}}); !a.Ready {
		t.Fatalf("docker cap should not hide linux: %+v", a)
	}
}
