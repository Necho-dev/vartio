package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"govmomi-wmks/internal/common"
	"govmomi-wmks/internal/guest"
)

func TestTunnelPolicy(t *testing.T) {
	cases := []struct {
		policy, target string
		ok             bool
	}{
		{"", "127.0.0.1:54345", true},
		{"", "localhost:9222", true},
		{"", "[::1]:9222", true},
		{"", "10.0.0.5:80", false},
		{"", "example.invalid:80", false},
		{"off", "127.0.0.1:54345", false},
		{"10.0.0.0/8", "10.1.2.3:80", true},
		{"10.0.0.0/8", "127.0.0.1:80", false},
		{"loopback,192.168.1.9:22", "192.168.1.9:22", true},
		{"loopback,192.168.1.9:22", "192.168.1.9:23", false},
		{"192.168.1.9:*", "192.168.1.9:8080", true},
		{"localhost:5000", "127.0.0.1:5000", true},
	}
	for _, c := range cases {
		_, err := parseTunnelPolicy(c.policy).resolve(c.target)
		if (err == nil) != c.ok {
			t.Errorf("policy %q target %q: err=%v want ok=%v", c.policy, c.target, err, c.ok)
		}
	}
}

func startLiveAgent(t *testing.T, bios string) (*guest.Hub, *httptest.Server) {
	t.Helper()
	h := guest.NewHub("dev")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)
	ws := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/probe/connect"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = serve(ctx, config{URL: ws, Token: "dev", BiosUUID: bios}) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !h.Online(bios) {
		time.Sleep(20 * time.Millisecond)
	}
	if !h.Online(bios) {
		t.Fatal("agent did not connect")
	}
	return h, srv
}

func tunnelClient(h *guest.Hub, key, target string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext:       func(ctx context.Context, _, _ string) (net.Conn, error) { return h.DialTCP(ctx, key, target, "test") },
		DisableKeepAlives: true,
	}, Timeout: 30 * time.Second}
}

func TestLiveTunnelHTTP(t *testing.T) {
	blob := make([]byte, 3<<20+17)
	_, _ = rand.Read(blob)
	sum := sha256.Sum256(blob)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/blob":
			_, _ = w.Write(blob)
		case "/echo":
			b, _ := io.ReadAll(r.Body)
			s := sha256.Sum256(b)
			fmt.Fprintf(w, "%d %s", len(b), hex.EncodeToString(s[:]))
		default:
			fmt.Fprint(w, "pong")
		}
	}))
	t.Cleanup(local.Close)
	target := strings.TrimPrefix(local.URL, "http://")

	h, _ := startLiveAgent(t, "live-tunnel")
	info, _ := h.Info("live-tunnel")
	if info.Primitives[guest.CapTunnelTCP] != 1 {
		t.Fatalf("primitives %+v", info.Primitives)
	}
	c := tunnelClient(h, "live-tunnel", target)

	resp, err := c.Get("http://x/blob")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if s := sha256.Sum256(got); s != sum {
		t.Fatalf("download corrupted: %d bytes", len(got))
	}

	resp, err = c.Post("http://x/echo", "application/octet-stream", bytes.NewReader(blob))
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if want := fmt.Sprintf("%d %s", len(blob), hex.EncodeToString(sum[:])); string(got) != want {
		t.Fatalf("upload: %s want %s", got, want)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := c.Get("http://x/ping")
			if err != nil {
				errs <- err
				return
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(b) != "pong" {
				errs <- fmt.Errorf("got %q", b)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(h.Tunnels("live-tunnel")) > 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if n := len(h.Tunnels("live-tunnel")); n != 0 {
		t.Fatalf("%d tunnels left open", n)
	}

	_, err = h.DialTCP(context.Background(), "live-tunnel", "10.255.255.1:80", "test")
	var api *common.Error
	if !errors.As(err, &api) || api.Code != "agent_policy" || api.Status != http.StatusForbidden {
		t.Fatalf("non-loopback target: %v", err)
	}
	_, err = h.DialTCP(context.Background(), "live-tunnel", "127.0.0.1:1", "test")
	if !errors.As(err, &api) || api.Code != "dial_failed" {
		t.Fatalf("closed port: %v", err)
	}

	conn, err := h.DialTCP(context.Background(), "live-tunnel", target, "plugin:x")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if n := h.CloseTunnels("plugin:x"); n != 1 {
		t.Fatalf("closed %d tunnels", n)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("tunnel still open after CloseTunnels: %v", err)
	}
}

func TestLiveTunnelDisabled(t *testing.T) {
	t.Setenv("GUEST_TUNNEL_ALLOW", "off")
	h, _ := startLiveAgent(t, "live-tunnel-off")
	_, err := h.DialTCP(context.Background(), "live-tunnel-off", "127.0.0.1:54345", "test")
	var api *common.Error
	if !errors.As(err, &api) || api.Code != "cap_missing" {
		t.Fatalf("want cap_missing, got %v", err)
	}
}
