package guest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/gorilla/websocket"
	"testing"
)

func TestDevHTTPHealthExecP1Confirm(t *testing.T) {
	h := NewHub("dev")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/probe/connect?token=dev"
	conn := dialHello(t, wsURL, Hello{BiosUUID: "http-1", Version: "t"})
	go fakeAgent(t, conn, map[string]any{
		MethodHealth:    Health{CPU: CPUInfo{LogicalCPUs: 4, Pct: 2}, Memory: MemInfo{TotalMB: 1024}},
		MethodExec:      ExecResult{ExitCode: 0, Stdout: "ok\n"},
		MethodPower:     PowerResult{Accepted: true, Action: "reboot"},
		MethodFilesList: FilesListResult{Path: "/tmp/probe-agent", Staging: "/tmp/probe-agent", Entries: []FileEntry{}},
	})
	waitOnline(t, h, "http-1")

	var sessions []SessionInfo
	getJSON(t, srv.URL+"/v1/probe/sessions", http.StatusOK, &sessions)
	if len(sessions) != 1 || !sessions[0].Online {
		t.Fatalf("sessions %+v", sessions)
	}

	var health Health
	getJSON(t, srv.URL+"/v1/probe/sessions/http-1/health", http.StatusOK, &health)
	if health.CPU.LogicalCPUs != 4 {
		t.Fatalf("health %+v", health)
	}

	var ex ExecResult
	postJSON(t, srv.URL+"/v1/probe/sessions/http-1/exec", map[string]any{"command": "hostname"}, http.StatusOK, &ex)
	if ex.Stdout != "ok\n" {
		t.Fatalf("exec %+v", ex)
	}

	var files FilesListResult
	getJSON(t, srv.URL+"/v1/probe/sessions/http-1/files", http.StatusOK, &files)
	if files.Entries == nil {
		t.Fatal("files.entries should be []")
	}
	var disp DisplayInfo
	getJSON(t, srv.URL+"/v1/probe/sessions/http-1/display", http.StatusOK, &disp)
	if !disp.ViewOnly || disp.FPS != DisplayFPS {
		t.Fatalf("display info %+v", disp)
	}

	code, body := rawPost(t, srv.URL+"/v1/probe/sessions/http-1/power", []byte(`{"action":"reboot"}`))
	if code != 403 || !strings.Contains(body, "confirm_required") {
		t.Fatalf("power no confirm %d %s", code, body)
	}

	var pwr PowerResult
	postJSON(t, srv.URL+"/v1/probe/sessions/http-1/power", map[string]any{"action": "reboot", "confirm": true}, http.StatusOK, &pwr)
	if !pwr.Accepted {
		t.Fatalf("power %+v", pwr)
	}

	code, body = rawGet(t, srv.URL+"/v1/probe/sessions/missing/health")
	if code != 409 || !strings.Contains(body, "guest_offline") {
		t.Fatalf("offline %d %s", code, body)
	}
}

func TestDisplayWSViewOnly(t *testing.T) {
	h := NewHub("dev")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/probe/connect?token=dev"
	conn := dialHello(t, wsURL, Hello{BiosUUID: "disp-1"})
	frame := []byte{0xff, 0xd8, 0xff, 0xd9}
	go func() {
		for {
			var fr Frame
			if err := conn.ReadJSON(&fr); err != nil {
				return
			}
			switch fr.Method {
			case MethodDisplayOpen:
				b, _ := json.Marshal(DisplayOpenResult{OK: true, FPS: 2, MaxSide: 1280})
				_ = conn.WriteJSON(Frame{ID: fr.ID, Result: b})
				_ = conn.WriteJSON(Frame{ID: fr.ID, Stream: "display", Data: frame})
			case MethodDisplayClose:
				_ = conn.WriteJSON(Frame{ID: fr.ID, Result: json.RawMessage(`{"ok":true}`)})
			}
		}
	}()
	waitOnline(t, h, "disp-1")
	ticket, err := h.IssueDisplay("disp-1")
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+ticket["ws"], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, frame) {
		t.Fatalf("frame %x", data)
	}
}

func TestOldGuestPathsGone(t *testing.T) {
	h := NewHub("dev")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)
	for _, p := range []string{"/v1/guest/connect", "/v1/guest/sessions", "/ws/guest-term", "/ws/guest-display"} {
		code, _ := rawGet(t, srv.URL+p)
		if code != http.StatusNotFound {
			t.Fatalf("%s want 404 got %d", p, code)
		}
	}
}

func TestConnectRejectsBadToken(t *testing.T) {
	h := NewHub("secret")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)
	_, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/probe/connect?token=nope", nil)
	if err == nil {
		t.Fatal("expected reject")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func getJSON(t *testing.T, url string, want int, dst any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("GET %s -> %d %s", url, resp.StatusCode, b)
	}
	if dst != nil {
		if err := json.Unmarshal(b, dst); err != nil {
			t.Fatal(err)
		}
	}
}

func postJSON(t *testing.T, url string, body any, want int, dst any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	code, b := rawPost(t, url, raw)
	if code != want {
		t.Fatalf("POST %s -> %d %s", url, code, b)
	}
	if dst != nil {
		if err := json.Unmarshal([]byte(b), dst); err != nil {
			t.Fatal(err)
		}
	}
}

func rawGet(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func rawPost(t *testing.T, url string, body []byte) (int, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
