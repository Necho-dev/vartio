package guest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"govmomi-wmks/internal/release"
)

func writeRelease(t *testing.T, dir, ver string, key ed25519.PrivateKey, body []byte) {
	t.Helper()
	sub := filepath.Join(dir, ver, "probe-agent-"+ver+"-linux-amd64")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "probe-agent"), body, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	a := release.Artifact{OS: "linux", Arch: "amd64", File: "probe-agent-" + ver + "-linux-amd64/probe-agent", Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
	a.Sig = release.Sign(key, ver, a)
	b, _ := json.Marshal(release.Manifest{Version: ver, Artifacts: []release.Artifact{a}})
	if err := os.WriteFile(filepath.Join(dir, ver, release.ManifestName), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReleasesLatestFollowsLink(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	writeRelease(t, dir, "1.9.0", key, []byte("a"))
	writeRelease(t, dir, "1.10.0", key, []byte("b"))
	_ = os.MkdirAll(filepath.Join(dir, "1.11.0"), 0o755) // unsigned: ignored
	rs := &Releases{Dir: dir}
	if got := rs.Latest().Version; got != "1.10.0" {
		t.Fatalf("Latest without link = %s", got)
	}
	if err := os.Symlink("1.9.0", filepath.Join(dir, "latest")); err != nil {
		t.Fatal(err)
	}
	if got := rs.Latest().Version; got != "1.9.0" {
		t.Fatalf("Latest with link = %s", got)
	}
	if len(rs.List()) != 2 {
		t.Fatalf("List = %d versions", len(rs.List()))
	}
	if _, err := rs.Get("../etc"); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestAgentLatestAndDownload(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	writeRelease(t, dir, "1.9.1", key, []byte("new build"))
	h := NewHub("tok")
	h.Releases = &Releases{Dir: dir}
	srv := httptest.NewServer(h.DevHandler())
	defer srv.Close()
	get := func(path string, token bool) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if token {
			req.Header.Set("X-Guest-Token", "tok")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	if r := get(PathAgentLatest+"?os=linux&arch=amd64&current=1.9.0", false); r.StatusCode != 401 {
		t.Fatalf("no token: %d", r.StatusCode)
	}
	if r := get(PathAgentLatest+"?os=linux&arch=amd64&current=1.9.1", true); r.StatusCode != 204 {
		t.Fatalf("current: %d", r.StatusCode)
	}
	if r := get(PathAgentLatest+"?os=windows&arch=amd64&current=1.9.0", true); r.StatusCode != 204 {
		t.Fatalf("other platform: %d", r.StatusCode)
	}
	r := get(PathAgentLatest+"?os=linux&arch=amd64&current=1.9.0", true)
	var l AgentLatest
	if r.StatusCode != 200 || json.NewDecoder(r.Body).Decode(&l) != nil || l.Version != "1.9.1" {
		t.Fatalf("latest: %d %+v", r.StatusCode, l)
	}
	d := get(l.URL, true)
	body := make([]byte, 64)
	n, _ := d.Body.Read(body)
	if d.StatusCode != 200 || string(body[:n]) != "new build" {
		t.Fatalf("download: %d %q", d.StatusCode, body[:n])
	}
	if r := get(l.URL, false); r.StatusCode != 401 {
		t.Fatalf("download without token: %d", r.StatusCode)
	}
}
