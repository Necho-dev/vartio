package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"govmomi-wmks/internal/common"
)

// tunnelAgent answers tunnel.open, echoes bytes upper-cased, closes the tunnel
// when it receives "bye", and reports hub-side closes on closed.
func tunnelAgent(conn *websocket.Conn, closed chan<- string) {
	var mu sync.Mutex
	write := func(fr Frame) {
		mu.Lock()
		_ = conn.WriteJSON(fr)
		mu.Unlock()
	}
	for {
		var fr Frame
		if err := conn.ReadJSON(&fr); err != nil {
			return
		}
		switch {
		case fr.Method == MethodTunnelOpen:
			var req TunnelOpenRequest
			_ = json.Unmarshal(fr.Params, &req)
			if req.Target == "10.0.0.1:80" {
				write(Frame{ID: fr.ID, Error: &FrameError{Code: "agent_policy", Message: "refused"}})
				continue
			}
			b, _ := json.Marshal(TunnelOpenResult{OK: true})
			write(Frame{ID: fr.ID, Result: b})
		case fr.Stream == StreamTunnel:
			if string(fr.Data) == "bye" {
				write(Frame{ID: fr.ID, Stream: StreamTunnel, Data: []byte("BYE")})
				write(Frame{ID: fr.ID, Stream: StreamTunnelClose})
				continue
			}
			write(Frame{ID: fr.ID, Stream: StreamTunnel, Data: bytes.ToUpper(fr.Data)})
		case fr.Stream == StreamTunnelClose:
			if closed != nil {
				closed <- fr.ID
			}
		}
	}
}

func tunnelHub(t *testing.T, bios string, caps []string) (*Hub, *websocket.Conn) {
	t.Helper()
	h := NewHub("")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)
	conn := dialHello(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/probe/connect", Hello{BiosUUID: bios, Caps: caps})
	waitOnline(t, h, bios)
	return h, conn
}

func TestTunnelEchoAndClose(t *testing.T) {
	h, conn := tunnelHub(t, "tun-1", []string{CapTunnelTCP})
	closed := make(chan string, 4)
	go tunnelAgent(conn, closed)

	c, err := h.DialTCP(context.Background(), "tun-1", "127.0.0.1:9", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, err := io.ReadAtLeast(c, buf, 5)
	if err != nil || string(buf[:n]) != "HELLO" {
		t.Fatalf("echo %q %v", buf[:n], err)
	}
	if list := h.Tunnels("tun-1"); len(list) != 1 || list[0].TxBytes != 5 || list[0].RxBytes != 5 || list[0].Owner != "test" {
		t.Fatalf("tunnels %+v", list)
	}
	_ = c.Close()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("agent never saw tun.close")
	}

	c2, err := h.DialTCP(context.Background(), "tun-1", "127.0.0.1:9", "test")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c2.Write([]byte("bye"))
	got, err := io.ReadAll(c2)
	if err != nil || string(got) != "BYE" {
		t.Fatalf("peer close: %q %v", got, err)
	}
	select {
	case id := <-closed:
		t.Fatalf("hub echoed close for peer-closed tunnel %s", id)
	case <-time.After(100 * time.Millisecond):
	}
	if n := len(h.Tunnels("tun-1")); n != 0 {
		t.Fatalf("%d tunnels left", n)
	}
}

func TestTunnelErrors(t *testing.T) {
	h, conn := tunnelHub(t, "tun-old", nil)
	go fakeAgent(t, conn, nil)
	_, err := h.DialTCP(context.Background(), "tun-old", "127.0.0.1:9", "test")
	var api *common.Error
	if !errors.As(err, &api) || api.Code != "cap_missing" {
		t.Fatalf("old agent: %v", err)
	}
	if _, err := h.DialTCP(context.Background(), "nobody", "127.0.0.1:9", "test"); !errors.As(err, &api) || api.Code != "guest_offline" {
		t.Fatalf("offline: %v", err)
	}
	if _, err := h.DialTCP(context.Background(), "tun-old", "no-port", "test"); !errors.As(err, &api) || api.Code != "bad_request" {
		t.Fatalf("bad target: %v", err)
	}

	h2, conn2 := tunnelHub(t, "tun-2", []string{CapTunnelTCP})
	go tunnelAgent(conn2, nil)
	if _, err := h2.DialTCP(context.Background(), "tun-2", "10.0.0.1:80", "test"); !errors.As(err, &api) || api.Code != "agent_policy" || api.Status != 403 {
		t.Fatalf("policy: %v", err)
	}
	if n := len(h2.Tunnels("tun-2")); n != 0 {
		t.Fatalf("refused tunnel still registered: %d", n)
	}
	var open []io.Closer
	for i := 0; i < MaxTunnels; i++ {
		c, err := h2.DialTCP(context.Background(), "tun-2", "127.0.0.1:9", "test")
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, c)
	}
	if _, err := h2.DialTCP(context.Background(), "tun-2", "127.0.0.1:9", "test"); !errors.As(err, &api) || api.Code != "too_many_tunnels" {
		t.Fatalf("cap: %v", err)
	}
	for _, c := range open {
		_ = c.Close()
	}
}

func TestTunnelSessionDrop(t *testing.T) {
	h, conn := tunnelHub(t, "tun-drop", []string{CapTunnelTCP})
	go tunnelAgent(conn, nil)
	c, err := h.DialTCP(context.Background(), "tun-drop", "127.0.0.1:9", "test")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(c)
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel stayed open after the agent went away")
	}
}
