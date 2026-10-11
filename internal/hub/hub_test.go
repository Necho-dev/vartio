package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"vartio/internal/common"
	"vartio/internal/entities"
)

func TestHubHealthAndReplaceAndOffline(t *testing.T) {
	h := NewHub("dev")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/probe/connect?token=dev"
	conn1 := dialHello(t, wsURL, entities.Hello{BiosUUID: "AA-BB", HostName: "one", Version: "t"})
	go fakeAgent(t, conn1, map[string]any{
		entities.MethodHealth: entities.Health{TimeUTC: "2026-01-01T00:00:00Z", CPU: entities.CPUInfo{LogicalCPUs: 2, Pct: 1.5}},
		entities.MethodExec:   entities.ExecResult{ExitCode: 0, Stdout: "ok\n"},
	})

	waitOnline(t, h, "aa-bb")

	got, err := h.Health(context.Background(), "AA-BB")
	if err != nil {
		t.Fatal(err)
	}
	if got.CPU.LogicalCPUs != 2 {
		t.Fatalf("health %+v", got)
	}

	ex, err := h.Exec(context.Background(), "aa-bb", entities.ExecRequest{Command: "echo"})
	if err != nil || ex.Stdout != "ok\n" {
		t.Fatalf("exec %+v %v", ex, err)
	}

	if ack := dialAck(t, wsURL, entities.Hello{BiosUUID: "aa-bb", HostName: "two"}); ack.OK || ack.Code != "device_conflict" {
		t.Fatalf("live duplicate must be refused, got %+v", ack)
	}

	_ = conn1.Close()
	waitOffline(t, h, "aa-bb")
	conn2 := dialHello(t, wsURL, entities.Hello{BiosUUID: "aa-bb", HostName: "two", Version: "t2"})
	go fakeAgent(t, conn2, map[string]any{
		entities.MethodHealth: entities.Health{CPU: entities.CPUInfo{LogicalCPUs: 8}},
	})
	waitOnline(t, h, "aa-bb")

	got, err = h.Health(context.Background(), "aa-bb")
	if err != nil {
		t.Fatal(err)
	}
	if got.CPU.LogicalCPUs != 8 {
		t.Fatalf("expected reconnected session, got %+v", got)
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
	var api *common.Error
	if !errors.As(err, &api) || api.Code != "guest_offline" {
		t.Fatalf("want guest_offline, got %v", err)
	}
}

// A session that stopped answering pings (agent gone, TCP half-open) is replaced.
func TestHubReplacesDeadSession(t *testing.T) {
	h := NewHub("")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/probe/connect"
	_ = dialHello(t, wsURL, entities.Hello{BiosUUID: "b1", DeviceID: "dev-1"}) // never reads: no pongs
	waitOnline(t, h, "dev-1")
	conn := dialHello(t, wsURL, entities.Hello{BiosUUID: "b1", DeviceID: "dev-1", HostName: "new"})
	go fakeAgent(t, conn, nil)
	if info, _ := h.Info("dev-1"); info.HostName != "new" {
		t.Fatalf("want replacement, got %+v", info)
	}
}

// Cloned VMs share a BIOS UUID but differ in device id: both stay online, the BIOS
// form becomes ambiguous and the device id still routes.
func TestHubDeviceIDAndSharedBIOS(t *testing.T) {
	h := NewHub("")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/probe/connect"
	c1 := dialHello(t, wsURL, entities.Hello{BiosUUID: "same", DeviceID: "AAA"})
	go fakeAgent(t, c1, map[string]any{entities.MethodHealth: entities.Health{CPU: entities.CPUInfo{LogicalCPUs: 1}}})
	waitOnline(t, h, "aaa")
	if got, err := h.Health(context.Background(), "same"); err != nil || got.CPU.LogicalCPUs != 1 {
		t.Fatalf("unique bios should resolve: %+v %v", got, err)
	}
	c2 := dialHello(t, wsURL, entities.Hello{BiosUUID: "same", DeviceID: "bbb"})
	go fakeAgent(t, c2, map[string]any{entities.MethodHealth: entities.Health{CPU: entities.CPUInfo{LogicalCPUs: 2}}})
	waitOnline(t, h, "bbb")
	if got, err := h.Health(context.Background(), "bbb"); err != nil || got.CPU.LogicalCPUs != 2 {
		t.Fatalf("device id should route: %+v %v", got, err)
	}
	_, err := h.Health(context.Background(), "same")
	var api *common.Error
	if !errors.As(err, &api) || api.Code != "guest_unbound" {
		t.Fatalf("shared bios want guest_unbound, got %v", err)
	}
	if len(h.List()) != 2 {
		t.Fatalf("both clones online, got %d", len(h.List()))
	}
}

func TestDeviceFactsMatches(t *testing.T) {
	a := &entities.DeviceFacts{MID: "m", HW: "h", MAC: "x"}
	if n := a.Matches(&entities.DeviceFacts{MID: "m", HW: "h", MAC: "y"}); n != 2 {
		t.Fatalf("want 2, got %d", n)
	}
	if n := a.Matches(&entities.DeviceFacts{HW: "h"}); n != 1 {
		t.Fatalf("empty parts must not match, got %d", n)
	}
}

func TestHubFilesOffline(t *testing.T) {
	h := NewHub("")
	_, err := h.Call(context.Background(), "x", entities.MethodFilesGet, nil)
	var api *common.Error
	if !errors.As(err, &api) || api.Code != "guest_offline" {
		t.Fatalf("want guest_offline, got %v", err)
	}
}

func TestHubFilesAndDisplayTicket(t *testing.T) {
	h := NewHub("dev")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/probe/connect?token=dev"
	conn := dialHello(t, wsURL, entities.Hello{BiosUUID: "file-1", Version: "t"})
	go fakeAgent(t, conn, map[string]any{
		entities.MethodFilesList:   entities.FilesListResult{Path: "/tmp/probe-agent", Staging: "/tmp/probe-agent", Entries: []entities.FileEntry{}},
		entities.MethodDisplayOpen: entities.DisplayOpenResult{OK: true, FPS: 2, MaxSide: 1280},
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
	var api *common.Error
	if !errors.As(err, &api) || api.Code != "guest_unbound" {
		t.Fatalf("want guest_unbound, got %v", err)
	}
}

func TestHubTimeout(t *testing.T) {
	h := NewHub("")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/probe/connect"
	conn := dialHello(t, wsURL, entities.Hello{BiosUUID: "slow"})
	go func() {
		for {
			var fr entities.Frame
			if err := conn.ReadJSON(&fr); err != nil {
				return
			}
			// never reply
		}
	}()
	waitOnline(t, h, "slow")
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err := h.Call(ctx, "slow", entities.MethodHealth, nil)
	if err == nil {
		t.Fatal("expected timeout")
	}
}

func TestConfirmRequired(t *testing.T) {
	h := NewHub("")
	_, err := h.Power(context.Background(), "x", "reboot", false)
	var api *common.Error
	if !errors.As(err, &api) || api.Code != "confirm_required" {
		t.Fatalf("got %v", err)
	}
}

func dialAck(t *testing.T, url string, hello entities.Hello) entities.HelloOK {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	var ack entities.HelloOK
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatal(err)
	}
	return ack
}

func waitOffline(t *testing.T, h *Hub, key string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !h.Online(key) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %s still online", key)
}

func dialHello(t *testing.T, url string, hello entities.Hello) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	var ok entities.HelloOK
	if err := conn.ReadJSON(&ok); err != nil || !ok.OK {
		t.Fatalf("hello ack %v %+v", err, ok)
	}
	return conn
}

func fakeAgent(t *testing.T, conn *websocket.Conn, replies map[string]any) {
	t.Helper()
	for {
		var fr entities.Frame
		if err := conn.ReadJSON(&fr); err != nil {
			return
		}
		if fr.Method == "" {
			continue
		}
		v, ok := replies[fr.Method]
		if !ok {
			_ = conn.WriteJSON(entities.Frame{ID: fr.ID, Error: &entities.FrameError{Code: "not_implemented", Message: fr.Method}})
			continue
		}
		b, _ := json.Marshal(v)
		_ = conn.WriteJSON(entities.Frame{ID: fr.ID, Result: b})
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
