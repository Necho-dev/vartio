package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"vartio/internal/entities"
)

const (
	tunnelDialTimeout = 5 * time.Second
	// tunnelStall bounds how long a local peer that stopped reading may hold up
	// the agent read loop; the tunnel is closed rather than dropping bytes.
	tunnelStall = 5 * time.Second
)

// tunnelPolicy is GUEST_TUNNEL_ALLOW: "off", or a comma list of "loopback",
// "host:port", "host:*" and CIDRs. Empty means loopback only.
type tunnelPolicy struct {
	off      bool
	loopback bool
	nets     []*net.IPNet
	hosts    map[string]bool // "ip:port" or "ip:*"
}

func loadTunnelPolicy() tunnelPolicy {
	return parseTunnelPolicy(getenv("VARTIO_AGENT_TUNNEL_ALLOW", "GUEST_TUNNEL_ALLOW"))
}

func parseTunnelPolicy(v string) tunnelPolicy {
	p := tunnelPolicy{hosts: map[string]bool{}}
	v = strings.TrimSpace(v)
	if strings.EqualFold(v, "off") {
		p.off = true
		return p
	}
	if v == "" {
		p.loopback = true
		return p
	}
	for _, e := range splitCSV(v) {
		switch {
		case strings.EqualFold(e, "loopback"):
			p.loopback = true
		case strings.Contains(e, "/"):
			if _, n, err := net.ParseCIDR(e); err == nil {
				p.nets = append(p.nets, n)
			}
		default:
			host, port, err := net.SplitHostPort(e)
			if err != nil {
				continue
			}
			if strings.EqualFold(host, "localhost") {
				host = "127.0.0.1"
			}
			if ip := net.ParseIP(host); ip != nil {
				p.hosts[net.JoinHostPort(ip.String(), port)] = true
			}
		}
	}
	return p
}

// resolve returns the address to dial, or an error if policy refuses target.
// Hostnames are resolved here and the resolved IP is what gets checked and dialed.
func (p tunnelPolicy) resolve(target string) (string, error) {
	if p.off {
		return "", fmt.Errorf("tunnels are disabled on this machine (GUEST_TUNNEL_ALLOW=off)")
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return "", fmt.Errorf("target must be host:port")
	}
	var ips []net.IP
	if strings.EqualFold(host, "localhost") {
		ips = []net.IP{net.IPv4(127, 0, 0, 1)}
	} else if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ips, err = net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return "", fmt.Errorf("resolve %s: %v", host, err)
		}
	}
	for _, ip := range ips {
		if p.allows(ip, port) {
			return net.JoinHostPort(ip.String(), port), nil
		}
	}
	return "", fmt.Errorf("target %s is not allowed by GUEST_TUNNEL_ALLOW (default: loopback only)", target)
}

func (p tunnelPolicy) allows(ip net.IP, port string) bool {
	if p.loopback && ip.IsLoopback() {
		return true
	}
	for _, n := range p.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return p.hosts[net.JoinHostPort(ip.String(), port)] || p.hosts[net.JoinHostPort(ip.String(), "*")]
}

type agentTunnel struct {
	conn net.Conn
	in   chan []byte
	done chan struct{}
	once sync.Once
}

func (t *agentTunnel) stop() {
	t.once.Do(func() {
		close(t.done)
		_ = t.conn.Close()
	})
}

type tunnelHub struct {
	policy tunnelPolicy
	mu     sync.Mutex
	m      map[string]*agentTunnel
}

func newTunnelHub() *tunnelHub {
	return &tunnelHub{policy: loadTunnelPolicy(), m: map[string]*agentTunnel{}}
}

func (a *agent) tunnelOpen(params json.RawMessage) (any, *entities.FrameError) {
	var req entities.TunnelOpenRequest
	if err := json.Unmarshal(params, &req); err != nil || req.ID == "" {
		return nil, bad("invalid params")
	}
	if req.Mode != "" && req.Mode != "tcp" {
		return nil, bad("mode must be tcp")
	}
	th := a.tun
	addr, err := th.policy.resolve(req.Target)
	if err != nil {
		return nil, &entities.FrameError{Code: "agent_policy", Message: err.Error()}
	}
	th.mu.Lock()
	n := len(th.m)
	th.mu.Unlock()
	if n >= entities.MaxTunnels {
		return nil, &entities.FrameError{Code: "too_many_tunnels", Message: fmt.Sprintf("at most %d tunnels", entities.MaxTunnels)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), tunnelDialTimeout)
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	cancel()
	if err != nil {
		return nil, &entities.FrameError{Code: "dial_failed", Message: err.Error()}
	}
	t := &agentTunnel{conn: conn, in: make(chan []byte, 256), done: make(chan struct{})}
	th.mu.Lock()
	th.m[req.ID] = t
	th.mu.Unlock()
	go a.tunnelPump(req.ID, t)
	return entities.TunnelOpenResult{OK: true, Remote: addr}, nil
}

// tunnelPump relays local bytes to the hub and hub bytes to the local peer.
func (a *agent) tunnelPump(id string, t *agentTunnel) {
	go func() {
		for {
			select {
			case b := <-t.in:
				if _, err := t.conn.Write(b); err != nil {
					t.stop()
					return
				}
			case <-t.done:
				return
			}
		}
	}()
	buf := make([]byte, entities.TunnelChunk)
	for {
		n, err := t.conn.Read(buf)
		if n > 0 {
			a.send(entities.Frame{ID: id, Stream: entities.StreamTunnel, Data: append([]byte(nil), buf[:n]...)})
		}
		if err != nil {
			break
		}
	}
	if a.tun.remove(id, t) {
		a.send(entities.Frame{ID: id, Stream: entities.StreamTunnelClose})
	}
	t.stop()
}

func (th *tunnelHub) remove(id string, t *agentTunnel) bool {
	th.mu.Lock()
	defer th.mu.Unlock()
	if th.m[id] != t {
		return false
	}
	delete(th.m, id)
	return true
}

func (th *tunnelHub) write(id string, data []byte) {
	th.mu.Lock()
	t := th.m[id]
	th.mu.Unlock()
	if t == nil || len(data) == 0 {
		return
	}
	select {
	case t.in <- data:
	case <-t.done:
	case <-time.After(tunnelStall):
		t.stop()
	}
}

// close ends one tunnel at the hub's request, or all of them when id is empty.
func (th *tunnelHub) close(id string) {
	th.mu.Lock()
	var list []*agentTunnel
	for k, t := range th.m {
		if id == "" || k == id {
			list = append(list, t)
			delete(th.m, k)
		}
	}
	th.mu.Unlock()
	for _, t := range list {
		t.stop()
	}
}
