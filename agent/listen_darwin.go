package agent

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os/exec"
	"strconv"

	"vartio/internal/entities"
)

// listenSockets asks lsof for listening TCP sockets only, which is much
// cheaper than gopsutil's full connection list. +c 0 keeps full command names.
func listenSockets(ctx context.Context) ([]entities.Listener, error) {
	b, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "+c", "0", "-nP", "-iTCP", "-sTCP:LISTEN", "-Fpcn").Output()
	if err != nil && len(b) == 0 {
		// lsof exits 1 when nothing matches.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	return parseLsof(b), nil
}

func parseLsof(b []byte) []entities.Listener {
	var out []entities.Listener
	var pid int32
	var name string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		v := line[1:]
		switch line[0] {
		case 'p':
			n, _ := strconv.Atoi(v)
			pid, name = int32(n), ""
		case 'c':
			name = unescapeLsof(v)
		case 'n':
			host, port, err := net.SplitHostPort(v)
			if err != nil {
				continue
			}
			p, err := strconv.Atoi(port)
			if err != nil {
				continue
			}
			if host == "*" {
				host = "0.0.0.0"
			}
			out = append(out, entities.Listener{IP: host, Port: p, PID: pid, Name: name})
		}
	}
	return out
}
