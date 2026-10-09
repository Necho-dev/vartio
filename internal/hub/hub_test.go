package guest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"govmomi-wmks/internal/esxi"
)

func TestHubHealthAndReplaceAndOffline(t *testing.T) {
	h := NewHub("dev")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/probe/connect?token=dev"
	conn1 := dialHello(t, wsURL, Hello{BiosUUID: "AA-BB", HostName: "one", Version: "t"})
	go fakeAgent(t, conn1, map[string]any{
		MethodHealth: Health{TimeUTC: "2026-01-01T00:00:00Z", CPU: CPUInfo{LogicalCPUs: 2, Pct: 1.5}},
		MethodExec:   ExecResult{ExitCode: 0, Stdout: "ok\n"},
	})

	waitOnline(t, h, "aa-bb")

	got, err := h.Health(context.Background(), "AA-BB")
	if err != nil {
		t.Fatal(err)
	}
	if got.CPU.LogicalCPUs != 2 {
		t.Fatalf("health %+v", got)
	}

	ex, err := h.Exec(context.Background(), "aa-bb", ExecRequest{Command: "echo"})
	if err != nil || ex.Stdout != "ok\n" {
		t.Fatalf("exec %+v %v", ex, err)
	}

	conn2 := dialHello(t, wsURL, Hello{BiosUUID: "aa-bb", HostName: "two", Version: "t2"})
	go fakeAgent(t, conn2, map[string]any{
		MethodHealth: Health{CPU: CPUInfo{LogicalCPUs: 8}},
	})
	waitOnline(t, h, "aa-bb")
	time.Sleep(50 * time.Millisecond)

	got, err = h.Health(context.Background(), "aa-bb")
	if err != nil {
		t.Fatal(err)
	}
	if got.CPU.LogicalCPUs != 8 {
		t.Fatalf("expected replacement session, got %+v", got)
	}

	_ = conn2.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !h.Online("aa-bb") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, err = h.Health(context.Background(), "aa-bb")
	if err == nil {
		t.Fatal("expected offline")
	}
	var api *esxi.Error
	if !errors.As(err, &api) || api.Code != "guest_offline" {
		t.Fatalf("want guest_offline, got %v", err)
	}
}

func TestHubFilesOffline(t *testing.T) {
	h := NewHub("")
	_, err := h.Call(context.Background(), "x", MethodFilesGet, nil)
	var api *esxi.Error
	if !errors.As(err, &api) || api.Code != "guest_offline" {
		t.Fatalf("want guest_offline, got %v", err)
	}
}

func TestHubFilesAndDisplayTicket(t *testing.T) {
	h := NewHub("dev")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/probe/connect?token=dev"
	conn := dialHello(t, wsURL, Hello{BiosUUID: "file-1", Version: "t"})
	go fakeAgent(t, conn, map[string]any{
		MethodFilesList:   FilesListResult{Path: "/tmp/probe-agent", Staging: "/tmp/probe-agent", Entries: []FileEntry{}},
		MethodDisplayOpen: DisplayOpenResult{OK: true, FPS: 2, MaxSide: 1280},
	})
	waitOnline(t, h, "file-1")

	list, err := h.FilesList(context.Background(), "file-1", "")
	if err != nil || list.Staging == "" {
		t.Fatalf("list %+v %v", list, err)
	}
	ticket, err := h.IssueDisplay("file-1")
	if err != nil || !strings.Contains(ticket["ws"], "/v1/probe/ws/display") {
		t.Fatalf("display ticket %+v %v", ticket, err)
	}
}

func TestHubAmbiguous(t *testing.T) {
	h := NewHub("")
	h.Ambiguous = func(bios string) bool { return bios == "shared" }
	_, err := h.Health(context.Background(), "shared")
	var api *esxi.Error
	if !errors.As(err, &api) || api.Code != "guest_unbound" {
		t.Fatalf("want guest_unbound, got %v", err)
	}
}

func TestHubTimeout(t *testing.T) {
	h := NewHub("")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/probe/connect"
	conn := dialHello(t, wsURL, Hello{BiosUUID: "slow"})
	go func() {
		for {
			var fr Frame
			if err := conn.ReadJSON(&fr); err != nil {
				return
			}
			// never reply
		}
	}()
	waitOnline(t, h, "slow")
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err := h.Call(ctx, "slow", MethodHealth, nil)
	if err == nil {
		t.Fatal("expected timeout")
	}
}

func TestConfirmRequired(t *testing.T) {
	h := NewHub("")
	_, err := h.Power(context.Background(), "x", "reboot", false)
	var api *esxi.Error
	if !errors.As(err, &api) || api.Code != "confirm_required" {
		t.Fatalf("got %v", err)
	}
}

func dialHello(t *testing.T, url string, hello Hello) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	var ok HelloOK
	if err := conn.ReadJSON(&ok); err != nil || !ok.OK {
		t.Fatalf("hello ack %v %+v", err, ok)
	}
	return conn
}

func fakeAgent(t *testing.T, conn *websocket.Conn, replies map[string]any) {
	t.Helper()
	for {
		var fr Frame
		if err := conn.ReadJSON(&fr); err != nil {
			return
		}
		if fr.Method == "" {
			continue
		}
		v, ok := replies[fr.Method]
		if !ok {
			_ = conn.WriteJSON(Frame{ID: fr.ID, Error: &FrameError{Code: "not_implemented", Message: fr.Method}})
			continue
		}
		b, _ := json.Marshal(v)
		_ = conn.WriteJSON(Frame{ID: fr.ID, Result: b})
	}
}

func waitOnline(t *testing.T, h *Hub, bios string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.Online(bios) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %s not online", bios)
}
