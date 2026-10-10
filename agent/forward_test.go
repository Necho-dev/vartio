package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"govmomi-wmks/internal/esxi"
	"govmomi-wmks/internal/guest"
)

func TestLiveForward(t *testing.T) {
	up := websocket.Upgrader{}
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			c, err := up.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			for {
				typ, msg, err := c.ReadMessage()
				if err != nil {
					return
				}
				_ = c.WriteMessage(typ, append([]byte("echo:"), msg...))
			}
		}
		fmt.Fprint(w, "pong")
	}))
	t.Cleanup(svc.Close)
	target := strings.TrimPrefix(svc.URL, "http://")

	h, _ := startLiveAgent(t, "live-forward")
	state := filepath.Join(t.TempDir(), "forwards.json")
	if err := h.LoadForwards(state); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f, err := h.AddForward(ctx, guest.ForwardRequest{Agent: "live-forward", Target: target, Note: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Port == 0 || f.Bind != "127.0.0.1" || !f.Listening || !f.Online {
		t.Fatalf("forward %+v", f)
	}
	t.Cleanup(func() { _, _ = h.RemoveForward(f.ID) })
	local := fmt.Sprintf("127.0.0.1:%d", f.Port)

	resp, err := http.Get("http://" + local + "/ping")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "pong" {
		t.Fatalf("http via forward: %q", b)
	}

	ws, _, err := websocket.DefaultDialer.Dial("ws://"+local+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.WriteMessage(websocket.TextMessage, []byte("hi"))
	if _, msg, err := ws.ReadMessage(); err != nil || string(msg) != "echo:hi" {
		t.Fatalf("websocket via forward: %q %v", msg, err)
	}
	if got := h.Forwards("live-forward"); len(got) != 1 || got[0].Conns < 1 || got[0].Total < 2 || got[0].RxBytes == 0 {
		t.Fatalf("stats %+v", got)
	}

	var api *esxi.Error
	_, err = h.AddForward(ctx, guest.ForwardRequest{Agent: "live-forward", Target: target, Port: f.Port})
	if !errors.As(err, &api) || api.Code != "port_taken" || api.Status != http.StatusConflict {
		t.Fatalf("duplicate port: %v", err)
	}
	_, err = h.AddForward(ctx, guest.ForwardRequest{Agent: "live-forward", Target: "127.0.0.1:1", Port: f.Port})
	if !errors.As(err, &api) || api.Code != "port_taken" {
		t.Fatalf("duplicate port is reported before the target test dial: %v", err)
	}
	other, _ := net.Listen("tcp", "127.0.0.1:0")
	defer other.Close()
	_, err = h.AddForward(ctx, guest.ForwardRequest{Agent: "live-forward", Target: target, Port: other.Addr().(*net.TCPAddr).Port, Bind: "0.0.0.0"})
	if !errors.As(err, &api) || api.Code != "port_taken" {
		t.Fatalf("port held by another program: %v", err)
	}
	_, err = h.AddForward(ctx, guest.ForwardRequest{Agent: "live-forward", Target: "10.255.255.1:80"})
	if !errors.As(err, &api) || api.Code != "agent_policy" {
		t.Fatalf("policy: %v", err)
	}
	_, err = h.AddForward(ctx, guest.ForwardRequest{Agent: "live-forward", Target: target, Bind: "192.168.1.1"})
	if !errors.As(err, &api) || api.Code != "bad_request" {
		t.Fatalf("bind: %v", err)
	}
	_, err = h.AddForward(ctx, guest.ForwardRequest{Agent: "nobody", Target: target})
	if !errors.As(err, &api) || api.Code != "guest_offline" {
		t.Fatalf("offline: %v", err)
	}

	var saved struct{ Forwards []guest.Forward }
	raw, _ := os.ReadFile(state)
	if json.Unmarshal(raw, &saved) != nil || len(saved.Forwards) != 1 || saved.Forwards[0].Port != f.Port || saved.Forwards[0].Target != target {
		t.Fatalf("state file: %s", raw)
	}

	if _, err := h.RemoveForward(f.ID); err != nil {
		t.Fatal(err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := ws.ReadMessage(); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("live connection survived removal: %v", err)
	}
	if c, err := net.DialTimeout("tcp", local, time.Second); err == nil {
		c.Close()
		t.Fatal("port still listening after removal")
	}
	raw, _ = os.ReadFile(state)
	if !strings.Contains(string(raw), `"forwards": []`) {
		t.Fatalf("state after removal: %s", raw)
	}
	if len(h.Forwards("")) != 0 {
		t.Fatal("rule still listed")
	}
}

func TestForwardReload(t *testing.T) {
	state := filepath.Join(t.TempDir(), "forwards.json")
	free, _ := net.Listen("tcp", "127.0.0.1:0")
	port := free.Addr().(*net.TCPAddr).Port
	free.Close()
	busy, _ := net.Listen("tcp", "127.0.0.1:0")
	defer busy.Close()
	_ = os.WriteFile(state, []byte(fmt.Sprintf(`{"forwards":[
		{"id":"a","agent":"x","bind":"127.0.0.1","port":%d,"target":"127.0.0.1:80"},
		{"id":"b","agent":"x","bind":"127.0.0.1","port":%d,"target":"127.0.0.1:81"}]}`, port, busy.Addr().(*net.TCPAddr).Port)), 0o600)
	h := guest.NewHub("dev")
	if err := h.LoadForwards(state); err != nil {
		t.Fatal(err)
	}
	got := h.Forwards("")
	if len(got) != 2 || !got[0].Listening || got[0].Online || got[1].Listening || got[1].Error == "" {
		t.Fatalf("reload %+v", got)
	}
	// An offline agent: connections are accepted and dropped, the rule stays.
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("offline agent: expected close")
	}
	c.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.Forwards("")[0].LastError == "" {
		time.Sleep(20 * time.Millisecond)
	}
	if h.Forwards("")[0].LastError == "" {
		t.Fatal("lastError not recorded")
	}
	for _, f := range got {
		_, _ = h.RemoveForward(f.ID)
	}
}
