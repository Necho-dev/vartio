package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"govmomi-wmks/internal/guest"
)

func TestLiveAgentHealthExecServices(t *testing.T) {
	h := guest.NewHub("dev")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)

	ws := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/probe/connect"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = serve(ctx, config{URL: ws, Token: "dev", BiosUUID: "live-macos"})
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !h.Online("live-macos") {
		time.Sleep(20 * time.Millisecond)
	}
	if !h.Online("live-macos") {
		t.Fatal("agent did not connect")
	}

	resp, err := http.Get(srv.URL + "/v1/probe/sessions/live-macos/health")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("health %d %s", resp.StatusCode, b)
	}
	var health guest.Health
	if err := json.Unmarshal(b, &health); err != nil {
		t.Fatal(err)
	}
	if health.CPU.LogicalCPUs < 1 || health.Memory.TotalMB < 1 {
		t.Fatalf("health snapshot %+v", health)
	}

	body, _ := json.Marshal(guest.ExecRequest{Command: "uname", Args: []string{"-s"}, Shell: "sh"})
	resp, err = http.Post(srv.URL+"/v1/probe/sessions/live-macos/exec", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	var ex guest.ExecResult
	if err := json.Unmarshal(b, &ex); err != nil {
		t.Fatal(err)
	}
	if ex.ExitCode != 0 || !strings.Contains(ex.Stdout, "Darwin") {
		t.Fatalf("exec %+v raw=%s", ex, b)
	}

	resp, err = http.Get(srv.URL + "/v1/probe/sessions/live-macos/services")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("services %d %s", resp.StatusCode, b)
	}
	var svcs []guest.ServiceInfo
	if err := json.Unmarshal(b, &svcs); err != nil {
		t.Fatal(err)
	}
	if svcs == nil {
		t.Fatal("services should be []")
	}

	stage := t.TempDir()
	t.Setenv("GUEST_FILE_STAGING", stage)
	putURL := srv.URL + "/v1/probe/sessions/live-macos/files?path=p1.txt&confirm=true"
	req, _ := http.NewRequest(http.MethodPut, putURL, strings.NewReader("p1-ok"))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("put file %d %s", resp.StatusCode, b)
	}

	resp, err = http.Get(srv.URL + "/v1/probe/sessions/live-macos/files")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "p1.txt") {
		t.Fatalf("list files %d %s", resp.StatusCode, b)
	}

	resp, err = http.Get(srv.URL + "/v1/probe/sessions/live-macos/files/content?path=p1.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "p1-ok" {
		t.Fatalf("get file %d %s", resp.StatusCode, b)
	}

	resp, err = http.Get(srv.URL + "/v1/probe/sessions/live-macos/display")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "viewOnly") {
		t.Fatalf("display info %d %s", resp.StatusCode, b)
	}
}
