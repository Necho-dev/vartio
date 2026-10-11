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
	"time"

	"vartio/internal/hub"
)

func TestValidateGit(t *testing.T) {
	for _, ok := range []string{
		"git@github.com:Necho-dev/vartio-plugin-chrome-devtools.git",
		"https://github.com/Necho-dev/vartio-plugin-bitbrowser.git",
		"https://github.com/Necho-dev/vartio-plugin-bitbrowser",
	} {
		if err := validateGit(ok); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "file:///tmp/plugin", "https://github.com/a/b;rm", "git@github.com:../etc"} {
		if err := validateGit(bad); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}

func TestCustomCatalogAndManual(t *testing.T) {
	doc := catalogDoc{CatalogVersion: 1}
	doc.Repository.ID = "lab"
	doc.Repository.Name = "Lab"
	doc.Plugins = []catalogPlugin{
		{Name: "hello", Title: "Hello", Version: "0.1.0", Git: "https://github.com/acme/vartio-plugin-hello.git", Description: "hi", Primitives: []string{"tunnel.tcp"}},
		{Name: "cdp", Title: "伪造", Git: "https://github.com/acme/fake.git"},
		{Name: "Bad_Name", Git: "https://github.com/acme/nope.git"},
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
	if err := r.addRepo(context.Background(), "lab", "Lab", srv.URL+"/index.json"); err != nil {
		t.Fatal(err)
	}
	if err := r.addRepo(context.Background(), "lab", "Lab", srv.URL); err == nil {
		t.Fatal("duplicate repo accepted")
	}
	item, ok := r.catalogBy("hello")
	if !ok || item.Title != "Hello" || item.Official || item.Origin != "Lab" {
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
	mux.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, PathPrefix+"/_manual", strings.NewReader(`{"name":"hello","git":"file:///tmp/x"}`)))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad git: %d %s", bad.Code, bad.Body.String())
	}
	clash := httptest.NewRecorder()
	mux.ServeHTTP(clash, httptest.NewRequest(http.MethodPost, PathPrefix+"/_manual", strings.NewReader(`{"name":"cdp","git":"https://github.com/acme/cdp.git"}`)))
	if clash.Code != http.StatusBadRequest {
		t.Fatalf("official clash: %d %s", clash.Code, clash.Body.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := r.addManual(ctx, "extra", "https://github.com/acme/vartio-plugin-extra.git"); err == nil {
		t.Fatal("clone of a missing repository succeeded")
	}
	if _, ok := r.catalogBy("extra"); !ok {
		t.Fatal("failed manual install was not remembered")
	}
	b, err := os.ReadFile(state)
	if err != nil || !strings.Contains(string(b), `"extra"`) || !strings.Contains(string(b), `"lab"`) {
		t.Fatalf("state: %v %s", err, b)
	}
	if err := r.removeRepo(context.Background(), "lab"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.catalogBy("hello"); ok {
		t.Fatal("removed repository still listed")
	}
}
