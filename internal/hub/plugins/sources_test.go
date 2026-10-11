package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/Necho-dev/vartio/sdk"
	"vartio/internal/hub"
)

func TestCustomCatalogAndManual(t *testing.T) {
	key, err := sdk.GenerateKey("lab-2026")
	if err != nil {
		t.Fatal(err)
	}
	doc := catalogDoc{CatalogVersion: 1}
	doc.Repository.ID = "lab"
	doc.Repository.Name = "Lab"
	doc.Plugins = []catalogPlugin{
		{Manifest: sdk.Manifest{
			ManifestVersion: 1, Name: "hello", Title: "Hello", Version: "0.1.0", Description: "hi",
			Engines: sdk.Engines{HostAPI: "1"}, Primitives: []string{"tunnel.tcp"}, Targets: []string{},
			Machines: []string{"linux"}, UI: false,
		}, Artifacts: []Artifact{{
			Target: "darwin-arm64", URL: "https://example.com/hello-0.1.0-darwin-arm64.vtp",
			SHA256: strings.Repeat("ab", 32), Size: 10, SigningKeyID: key.ID,
		}}},
		{Manifest: sdk.Manifest{
			ManifestVersion: 1, Name: "cdp", Title: "伪造", Version: "9.9.9",
			Engines: sdk.Engines{HostAPI: "1"}, Primitives: []string{"tunnel.tcp"}, Targets: []string{},
			Machines: []string{"linux"},
		}, Artifacts: []Artifact{{
			Target: "darwin-arm64", URL: "https://example.com/cdp.vtp",
			SHA256: strings.Repeat("cd", 32), Size: 10, SigningKeyID: key.ID,
		}}},
		{Manifest: sdk.Manifest{Name: "Bad_Name"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(doc)
	}))
	defer srv.Close()

	r := NewRegistry(hub.NewHub(""))
	r.Dir = t.TempDir()
	state := filepath.Join(t.TempDir(), "plugins.json")
	if err := r.Load(state, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.addRepo(context.Background(), "lab", "Lab", srv.URL+"/index.json", key.ID, key.PublicString()); err != nil {
		t.Fatal(err)
	}
	if err := r.addRepo(context.Background(), "lab", "Lab", srv.URL, key.ID, key.PublicString()); err == nil {
		t.Fatal("duplicate repo accepted")
	}
	item, ok := r.catalogBy("hello")
	if !ok || item.Title != "Hello" || item.Official || item.Origin != "Lab" || len(item.Machines) != 1 {
		t.Fatalf("hello: %+v %v", item, ok)
	}
	off, ok := r.catalogBy("cdp")
	if !ok || !off.Official || off.Title != "Chrome DevTools" {
		t.Fatalf("official shadowed: %+v", off)
	}
	if _, ok := r.catalogBy("Bad_Name"); ok {
		t.Fatal("invalid plugin name imported")
	}

	mux := http.NewServeMux()
	r.Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PathPrefix+"/_sources", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"lab"`) || !strings.Contains(rec.Body.String(), `"plugins":1`) {
		t.Fatalf("sources: %d %s", rec.Code, rec.Body.String())
	}
	var listed []listItem
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PathPrefix, nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	var sawHello, sawCDP bool
	for _, it := range listed {
		if it.Name == "hello" && !it.Official && it.Origin == "Lab" {
			sawHello = true
		}
		if it.Name == "cdp" && it.Official {
			sawCDP = true
		}
	}
	if !sawHello || !sawCDP {
		t.Fatalf("list: %+v", listed)
	}

	bad := httptest.NewRecorder()
	mux.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, PathPrefix+"/_manual", strings.NewReader(`{"name":"hello","url":"file:///tmp/x","sha256":"`+strings.Repeat("ab", 32)+`","signingKeyId":"lab-2026"}`)))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad url: %d %s", bad.Code, bad.Body.String())
	}
	clash := httptest.NewRecorder()
	mux.ServeHTTP(clash, httptest.NewRequest(http.MethodPost, PathPrefix+"/_manual", strings.NewReader(`{"name":"cdp","url":"https://example.com/cdp.vtp","sha256":"`+strings.Repeat("ab", 32)+`","signingKeyId":"lab-2026"}`)))
	if clash.Code != http.StatusBadRequest {
		t.Fatalf("official clash: %d %s", clash.Code, clash.Body.String())
	}

	if err := r.addManual(context.Background(), "extra", "http://127.0.0.1:1/nope.vtp", strings.Repeat("ab", 32), key.ID); err == nil {
		t.Fatal("download of a missing package succeeded")
	}
	if _, ok := r.catalogBy("extra"); ok {
		t.Fatal("failed manual install was remembered")
	}
	b, err := os.ReadFile(state)
	if err != nil || strings.Contains(string(b), `"extra"`) || !strings.Contains(string(b), `"lab"`) {
		t.Fatalf("state: %v %s", err, b)
	}
	if err := r.removeRepo(context.Background(), "lab"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.catalogBy("hello"); ok {
		t.Fatal("removed repository still listed")
	}
}
