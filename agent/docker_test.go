package agent

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"vartio/internal/entities"
)

func TestDemuxLogs(t *testing.T) {
	var raw bytes.Buffer
	frame := func(stream byte, s string) {
		hdr := [8]byte{stream}
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(s)))
		raw.Write(hdr[:])
		raw.WriteString(s)
	}
	frame(1, "out line\n")
	frame(2, "err line\n")
	frame(1, "")
	var got []byte
	w := &crlfWriter{out: func(p []byte) { got = append(got, p...) }}
	_ = demuxLogs(&raw, w)
	if string(got) != "out line\r\nerr line\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestCRLFAcrossChunks(t *testing.T) {
	var got []byte
	w := &crlfWriter{out: func(p []byte) { got = append(got, p...) }}
	_, _ = w.Write([]byte("a\r"))
	_, _ = w.Write([]byte("\nb\n"))
	if string(got) != "a\r\nb\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestHealthAndStats(t *testing.T) {
	for in, want := range map[string]string{"Up 2 hours (healthy)": "healthy", "Up 1 second (health: starting)": "starting",
		"Up 3 days (unhealthy)": "unhealthy", "Exited (0) 3 weeks ago": ""} {
		if got := healthOf(in); got != want {
			t.Errorf("healthOf(%q) = %q, want %q", in, got, want)
		}
	}
	var st apiStats
	_ = json.Unmarshal([]byte(`{"cpu_stats":{"cpu_usage":{"total_usage":700},"system_cpu_usage":9000,"online_cpus":8},
		"memory_stats":{"usage":130985984,"limit":8319770624,"stats":{"inactive_file":10366976}},
		"networks":{"eth0":{"rx_bytes":10,"tx_bytes":20},"eth1":{"rx_bytes":1,"tx_bytes":2}},
		"blkio_stats":{"io_service_bytes_recursive":[{"op":"read","value":5},{"op":"Write","value":7},{"op":"read","value":1}]},
		"pids_stats":{"current":17}}`), &st)
	var c entities.ContainerStat
	applyStats(&c, st, engineInfo{major: 29})
	if !c.Stats || c.CPUNs != 700 || c.SysNs != 9000 || c.CPUs != 8 || c.MemUsed != 130985984-10366976 || c.MemLimit != 8319770624 ||
		c.NetRx != 11 || c.NetTx != 22 || c.BlkRead != 6 || c.BlkWrite != 7 || c.Pids != 17 {
		t.Fatalf("stats %+v", c)
	}
	s := containerStat(apiContainer{ID: "246ba4243031aaaa", Names: []string{"/db"}, Image: "sha256:0123456789abcdef", State: "running",
		Status: "Up 1 hour (healthy)", Labels: map[string]string{
			"com.docker.compose.project": "app", "com.docker.compose.service": "db",
			"com.docker.compose.project.config_files": "/opt/app/compose.yml,/opt/app/compose.override.yml"},
		Ports: []apiPort{{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"}, {IP: "::", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
			{IP: "127.0.0.1", PrivatePort: 5432, PublicPort: 5432, Type: "tcp"}, {PrivatePort: 9000, Type: "tcp"}}})
	if s.ID != "246ba4243031" || s.Name != "db" || s.Image != "0123456789ab" || s.Health != "healthy" || s.Project != "app" ||
		s.Service != "db" || s.Compose != "/opt/app/compose.yml" ||
		s.Ports != "127.0.0.1:5432→5432/tcp, 8080→80/tcp" {
		t.Fatalf("container %+v", s)
	}
	var lh apiContainer
	_ = json.Unmarshal([]byte(`{"Id":"x","Names":["/a"],"Status":"Up 1 minute","Health":{"Status":"unhealthy","FailingStreak":3}}`), &lh)
	if containerStat(lh).Health != "unhealthy" {
		t.Fatal("list health (API 1.52) ignored")
	}
}

func TestEngineQuirks(t *testing.T) {
	podman := parseEngine("Libpod/5.2.0 (linux)", "", "5.2.0", "linux", []struct{ Name string }{{Name: "Podman Engine"}})
	if !podman.podman || podman.major != 0 || podman.name != "Podman Engine" {
		t.Fatalf("podman %+v", podman)
	}
	if d := parseEngine("Docker/24.0.7 (linux)", "Docker Engine - Community", "24.0.7", "linux", nil); d.podman || d.major != 24 {
		t.Fatalf("docker 24 %+v", d)
	}
	read := `"read":"2026-10-10T15:00:00.5Z"`
	var st apiStats
	_ = json.Unmarshal([]byte(`{`+read+`,"cpu_stats":{"cpu_usage":{"total_usage":5000},"system_cpu_usage":777,"online_cpus":4},"memory_stats":{"usage":100}}`), &st)
	var c entities.ContainerStat
	applyStats(&c, st, podman)
	if c.SysNs != 0 || c.CPUNs != 5000 || c.ReadNs == 0 {
		t.Fatalf("podman stats must use wall time: %+v", c)
	}
	st = apiStats{}
	_ = json.Unmarshal([]byte(`{`+read+`,"num_procs":2,"cpu_stats":{"cpu_usage":{"total_usage":30}},"memory_stats":{"privateworkingset":4096}}`), &st)
	c = entities.ContainerStat{}
	applyStats(&c, st, engineInfo{windows: true})
	if c.CPUNs != 3000 || c.SysNs != 0 || c.CPUs != 2 || c.MemUsed != 4096 {
		t.Fatalf("windows stats %+v", c)
	}
}

func TestExcludeAndImageRefs(t *testing.T) {
	t.Setenv("GUEST_DOCKER_EXCLUDE", "buildx_*, *-tmp")
	for name, want := range map[string]bool{"/buildx_builder0": true, "job-tmp": true, "web": false} {
		if dockerExcluded(name) != want {
			t.Errorf("excluded(%q) != %v", name, want)
		}
	}
	for in, want := range map[string][3]string{
		"nginx":                     {"docker.io", "library/nginx", "latest"},
		"postgres:18-alpine":        {"docker.io", "library/postgres", "18-alpine"},
		"grafana/grafana":           {"docker.io", "grafana/grafana", "latest"},
		"ghcr.io/owner/app:1.2":     {"ghcr.io", "owner/app", "1.2"},
		"localhost:5000/team/x:dev": {"localhost:5000", "team/x", "dev"},
	} {
		r, repo, tag, ok := parseImageRef(in)
		if !ok || [3]string{r, repo, tag} != want {
			t.Errorf("parseImageRef(%q) = %s %s %s %v", in, r, repo, tag, ok)
		}
	}
	for _, in := range []string{"nginx@sha256:abc", "sha256:0123", ""} {
		if _, _, _, ok := parseImageRef(in); ok {
			t.Errorf("parseImageRef(%q) should be skipped", in)
		}
	}
	p := parseChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/nginx:pull,push"`)
	if p["realm"] != "https://auth.docker.io/token" || p["service"] != "registry.docker.io" || p["scope"] != "repository:library/nginx:pull,push" {
		t.Fatalf("challenge %v", p)
	}
}

func TestSanitizeInspect(t *testing.T) {
	doc, err := sanitizeInspect(strings.NewReader(`{"Name":"/db","SizeRw":9007199254740993,"Config":{"Env":["PGPASSWORD=s3cret"],"Image":"postgres"}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(doc)
	if strings.Contains(string(b), "s3cret") || !strings.Contains(string(b), `"Image":"postgres"`) || !strings.Contains(string(b), "9007199254740993") {
		t.Fatalf("inspect %s", b)
	}
	t.Setenv("GUEST_DOCKER_EXCLUDE", "db")
	if _, err := sanitizeInspect(strings.NewReader(`{"Name":"/db"}`)); err != errExcluded {
		t.Fatalf("excluded err %v", err)
	}
}

func TestRegistryDigest(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:library/nginx:pull" {
				http.Error(w, "scope", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"token":"t0k"}`))
		case r.Header.Get("Authorization") != "Bearer t0k":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="reg"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.Method == http.MethodHead && r.URL.Path == "/v2/library/nginx/manifests/latest":
			w.Header().Set("Docker-Content-Digest", "sha256:new")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	registryScheme = "http"
	defer func() { registryScheme = "https" }()
	got, err := registryDigest(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "library/nginx", "latest")
	if err != nil || got != "sha256:new" {
		t.Fatalf("digest %q %v", got, err)
	}
}

// TestLiveDocker runs against the local engine with a throwaway alpine container.
func TestLiveDocker(t *testing.T) {
	if docker() == nil {
		t.Skip("no docker engine")
	}
	if exec.Command("docker", "image", "inspect", "alpine").Run() != nil {
		t.Skip("alpine image not present")
	}
	name := fmt.Sprintf("probe-agent-test-%d", time.Now().UnixNano())
	if out, err := exec.Command("docker", "run", "-d", "--name", name, "alpine", "sh", "-c",
		"i=0; while true; do echo tick $i; echo oops $i >&2; i=$((i+1)); sleep 0.2; done").CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info := dockerMetrics(ctx)
	if info == nil || info.Error != "" || info.Version == "" || info.Running < 1 {
		t.Fatalf("metrics %+v", info)
	}
	var mine *entities.ContainerStat
	for i := range info.Containers {
		if info.Containers[i].Name == name {
			mine = &info.Containers[i]
		}
	}
	if mine == nil || mine.State != "running" || !mine.Stats || mine.CPUNs == 0 || mine.MemUsed == 0 || mine.Pids < 1 {
		t.Fatalf("container %+v", mine)
	}

	t.Run("logs", func(t *testing.T) {
		var mu sync.Mutex
		var buf bytes.Buffer
		_, closer, _, err := startDockerLogs(entities.TermOpenRequest{Container: name, Tail: 5}, func(p []byte) {
			mu.Lock()
			buf.Write(p)
			mu.Unlock()
		}, func() {})
		if err != nil {
			t.Fatal(err)
		}
		defer closer()
		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return strings.Contains(buf.String(), "tick") && strings.Contains(buf.String(), "oops") && strings.Count(buf.String(), "\r\n") >= 12
		})
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(strings.ReplaceAll(buf.String(), "\r\n", ""), "\n") {
			t.Fatalf("bare LF in %q", buf.String())
		}
		if _, _, _, err := startDockerLogs(entities.TermOpenRequest{Container: "no-such-container-xyz"}, func([]byte) {}, func() {}); err == nil {
			t.Fatal("missing container: want error")
		}
	})

	t.Run("exec", func(t *testing.T) {
		t.Setenv("GUEST_DOCKER", "ro")
		if _, _, _, err := startDockerExec(entities.TermOpenRequest{Container: name}, 80, 24, func([]byte) {}, func() {}); err == nil {
			t.Fatal("ro: want agent_policy")
		}
		t.Setenv("GUEST_DOCKER", "rw")
		var mu sync.Mutex
		var buf bytes.Buffer
		exited := make(chan struct{})
		stdin, closer, resize, err := startDockerExec(entities.TermOpenRequest{Container: name}, 100, 30, func(p []byte) {
			mu.Lock()
			buf.Write(p)
			mu.Unlock()
		}, func() { close(exited) })
		if err != nil {
			t.Fatal(err)
		}
		var top string
		defer func() {
			if t.Failed() {
				mu.Lock()
				t.Logf("docker top:\n%s\nterminal: %q", top, buf.String())
				mu.Unlock()
			}
		}()
		resize(132, 40)
		time.Sleep(300 * time.Millisecond)
		_, _ = stdin.Write([]byte("sleep 777 & stty size; echo marker-$((6*7))\r"))
		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return strings.Contains(buf.String(), "marker-42") && strings.Contains(buf.String(), "40 132")
		})
		closer()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Fatal("no exit after close")
		}
		// The shell must not outlive the session.
		waitFor(t, func() bool {
			out, _ := exec.Command("docker", "top", name, "-o", "pid,args").Output()
			top = string(out)
			for _, l := range strings.Split(top, "\n")[1:] {
				if f := strings.Fields(l); len(f) > 1 && (strings.Contains(l, "777") || f[1] != "sleep" && !strings.Contains(l, "while true")) {
					return false
				}
			}
			return true
		})
	})
}

func TestComposeAndImageNames(t *testing.T) {
	if got := firstComposeFile(" /a/compose.yml , /a/override.yml "); got != "/a/compose.yml" {
		t.Fatalf("first %q", got)
	}
	if imageName(apiImage{ID: "sha256:0123456789abcdef", RepoTags: []string{"<none>:<none>"}}) != "0123456789ab" {
		t.Fatal("dangling")
	}
	if imageName(apiImage{RepoTags: []string{"nginx:latest", "nginx:1"}}) != "nginx:latest" {
		t.Fatal("tag")
	}
	cands := composeCandidates("/opt/app/compose.yml")
	if cands[0] != "/opt/app/compose.yml" {
		t.Fatalf("cands %v", cands)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("condition not met in time")
}
