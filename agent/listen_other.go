//go:build !darwin

package agent

import (
	"context"

	psnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"

	"vartio/internal/entities"
)

func listenSockets(ctx context.Context) ([]entities.Listener, error) {
	conns, err := psnet.ConnectionsWithContext(ctx, "tcp")
	if err != nil {
		return nil, err
	}
	names := procNames()
	cache := map[int32]string{}
	nameOf := func(pid int32) string {
		if n, ok := cache[pid]; ok {
			return n
		}
		n := names[pid]
		if p, err := process.NewProcessWithContext(ctx, pid); err == nil {
			if v, err := p.NameWithContext(ctx); err == nil && v != "" {
				n = v
			}
		}
		cache[pid] = n
		return n
	}
	var out []entities.Listener
	for _, c := range conns {
		if c.Status != "LISTEN" {
			continue
		}
		l := entities.Listener{IP: c.Laddr.IP, Port: int(c.Laddr.Port), PID: c.Pid}
		if c.Pid > 0 {
			l.Name = nameOf(c.Pid)
		}
		out = append(out, l)
	}
	return out, nil
}
