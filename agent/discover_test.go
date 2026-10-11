package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"vartio/internal/common"
	"vartio/internal/entities"
	"vartio/internal/hub"
)

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	t.Setenv("PROBE_X", "/opt/x")
	cases := map[string]string{
		"~/a":               filepath.Join(home, "a"),
		"%PROBE_X%/b":       filepath.FromSlash("/opt/x/b"),
		"${PROBE_X}/c":      filepath.FromSlash("/opt/x/c"),
		"%PROBE_MISSING%/d": filepath.FromSlash("%PROBE_MISSING%/d"),
	}
	for in, want := range cases {
		if got := expandPath(in); got != want {
			t.Errorf("expandPath(%q) = %q want %q", in, got, want)
		}
	}
	if got := expandArg("--user-data-dir=~/p"); got != "--user-data-dir="+filepath.Join(home, "p") {
		t.Errorf("expandArg: %q", got)
	}
	for _, keep := range []string{"--remote-debugging-port=9222", "https://example.com/a", `echo "$HOME" > "$0"`} {
		if got := expandArg(keep); got != keep {
			t.Errorf("expandArg(%q) = %q", keep, got)
		}
	}
	if got := expandArg("--x=%PROBE_X%"); got != "--x=/opt/x" {
		t.Errorf("expandArg %%VAR%%: %q", got)
	}
}

func call[T any](t *testing.T, h *hub.Hub, key, prim string, params any) (T, error) {
	t.Helper()
	var v T
	raw, err := h.CallPrimitive(context.Background(), key, prim, params)
	if err != nil {
		return v, err
	}
	return v, json.Unmarshal(raw, &v)
}

func TestLivePrimitives(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	h, _ := startLiveAgent(t, "live-prims")
	info, _ := h.Info("live-prims")
	for _, c := range []string{entities.CapFsWhich, entities.CapFsRead, entities.CapProcSpawn, entities.CapNetListeners} {
		if info.Primitives[c] != 1 {
			t.Fatalf("primitive %s not declared: %+v", c, info.Primitives)
		}
	}

	w, err := call[entities.WhichResult](t, h, "live-prims", entities.CapFsWhich, entities.WhichRequest{Paths: []string{"/bin/sh", "/no/such/browser"}, Names: []string{"sh", "ls"}})
	// PATH "sh" is /bin/sh again and is reported once.
	if err != nil || len(w.Hits) != 2 || w.Hits[0].Path != "/bin/sh" || w.Hits[1].Query != "ls" {
		t.Fatalf("which %+v %v", w, err)
	}

	small := filepath.Join(t.TempDir(), "DevToolsActivePort")
	_ = os.WriteFile(small, []byte("9222\n/devtools/browser/x\n"), 0o644)
	rd, err := call[entities.ReadResult](t, h, "live-prims", entities.CapFsRead, entities.ReadRequest{Paths: []string{small, "/no/such/file"}, Limit: 4})
	if err != nil || len(rd.Files) != 1 || string(rd.Files[0].Data) != "9222" || !rd.Files[0].Truncated || rd.Files[0].Size != 25 {
		t.Fatalf("read %+v %v", rd, err)
	}

	mark := filepath.Join(t.TempDir(), "spawned")
	sp, err := call[entities.SpawnResult](t, h, "live-prims", entities.CapProcSpawn, entities.SpawnRequest{Path: "sh", Args: []string{"-c", `echo "$PROBE_SPAWN" > "$0"`, mark}, Env: map[string]string{"PROBE_SPAWN": "ok"}})
	if err != nil || sp.PID <= 0 || !strings.HasSuffix(sp.Path, "/sh") {
		t.Fatalf("spawn %+v %v", sp, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		if got, _ = os.ReadFile(mark); strings.TrimSpace(string(got)) == "ok" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if strings.TrimSpace(string(got)) != "ok" {
		t.Fatalf("spawned process did not run: %q", got)
	}
	_, err = call[entities.SpawnResult](t, h, "live-prims", entities.CapProcSpawn, entities.SpawnRequest{Path: "/no/such/program"})
	var api *common.Error
	if !errors.As(err, &api) || api.Code != "not_found" {
		t.Fatalf("missing program: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	exe, _ := os.Executable()
	ls, err := call[[]entities.Listener](t, h, "live-prims", entities.CapNetListeners, entities.ListenersRequest{Names: []string{filepath.Base(exe)}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range ls {
		if l.Port == port && l.IP == "127.0.0.1" && l.PID == int32(os.Getpid()) {
			found = true
		}
	}
	if !found {
		t.Fatalf("own listener on %d not reported: %+v", port, ls)
	}
	if ls, _ := call[[]entities.Listener](t, h, "live-prims", entities.CapNetListeners, entities.ListenersRequest{Names: []string{"no-such-process-name"}}); len(ls) != 0 {
		t.Fatalf("name filter ignored: %+v", ls)
	}
}

func TestLiveSpawnDisabled(t *testing.T) {
	t.Setenv("GUEST_SPAWN", "off")
	h, _ := startLiveAgent(t, "live-spawn-off")
	_, err := h.CallPrimitive(context.Background(), "live-spawn-off", entities.CapProcSpawn, entities.SpawnRequest{Path: "sh"})
	var api *common.Error
	if !errors.As(err, &api) || api.Code != "cap_missing" {
		t.Fatalf("want cap_missing, got %v", err)
	}
}
