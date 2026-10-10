package guest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestUpgradeProgress(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	writeRelease(t, dir, "1.9.1", key, []byte("new build"))
	h := NewHub("tok")
	h.Releases = &Releases{Dir: dir}
	srv := httptest.NewServer(h.DevHandler())
	defer srv.Close()
	ws := "ws" + strings.TrimPrefix(srv.URL, "http") + PathConnect + "?token=tok"
	old := Hello{BiosUUID: "up-1", Version: "1.9.0", OS: "linux", Arch: "amd64", Caps: []string{CapUpgrade}}
	conn := dialHello(t, ws, old)
	sawDownload := make(chan struct{})
	go func() {
		for {
			var fr Frame
			if err := conn.ReadJSON(&fr); err != nil {
				return
			}
			if fr.Method != MethodUpgrade {
				continue
			}
			b, _ := json.Marshal(UpgradeStep{Stage: UpgradeDownload, Done: 4, Total: 9})
			_ = conn.WriteJSON(Frame{ID: fr.ID, Stream: StreamUpgrade, Data: b})
			<-sawDownload
			res, _ := json.Marshal(UpgradeResult{OK: true, From: "1.9.0", To: "1.9.1"})
			_ = conn.WriteJSON(Frame{ID: fr.ID, Result: res})
		}
	}()
	waitOnline(t, h, "up-1")
	id := h.Resolve("up-1")
	done := make(chan error, 1)
	go func() { _, err := h.Upgrade(context.Background(), id, ""); done <- err }()
	stage := func() UpgradeProgress {
		for _, p := range h.Upgrades() {
			if p.ID == id {
				return p
			}
		}
		return UpgradeProgress{}
	}
	waitFor := func(want string) UpgradeProgress {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if p := stage(); p.Stage == want {
				return p
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("stage %q never reached, at %+v", want, stage())
		return UpgradeProgress{}
	}
	if p := waitFor(UpgradeDownload); p.Done != 4 || p.Total != 9 || p.To != "1.9.1" {
		t.Fatalf("download progress %+v", p)
	}
	close(sawDownload)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitFor(UpgradeRestart)
	if _, err := h.Upgrade(context.Background(), id, ""); err == nil {
		t.Fatal("second upgrade accepted while restarting")
	}
	conn.Close()
	next := old
	next.Version, next.Upgrade = "1.9.1", &UpgradeReport{From: "1.9.0", To: "1.9.1", OK: true}
	dialHello(t, ws, next)
	waitFor(UpgradeDone)
}
