package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"

	"govmomi-wmks/internal/guest"
)

// tcpStats reads /proc directly; gopsutil's connection list walks every
// process's fds, which is far too slow for a 10s sample.
func tcpStats(context.Context) (*guest.TCPStats, error) {
	out := &guest.TCPStats{}
	ok := false
	for _, p := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		if countTCPStates(p, out) == nil {
			ok = true
		}
	}
	if !ok {
		return nil, errors.New("no /proc/net/tcp")
	}
	out.UDP = countLines("/proc/net/udp", "/proc/net/udp6", "/proc/net/udplite", "/proc/net/udplite6")
	out.Other = countLines("/proc/net/raw", "/proc/net/raw6", "/proc/net/icmp", "/proc/net/icmp6")
	out.OutSegs, out.Retrans = snmpTCP("/proc/net/snmp")
	return out, nil
}

func countTCPStates(path string, out *guest.TCPStats) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	sc.Scan() // header
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 4 {
			continue
		}
		out.Total++
		switch fs[3] {
		case "01":
			out.Established++
		case "06":
			out.TimeWait++
		case "0A":
			out.Listen++
		}
	}
	return sc.Err()
}

// countLines counts socket rows (one header line each) across /proc/net tables;
// missing or unreadable tables count as empty.
func countLines(paths ...string) int {
	n := 0
	for _, p := range paths {
		if c, err := countRows(p); err == nil {
			n += c
		}
	}
	return n
}

func countRows(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for i := 0; sc.Scan(); i++ {
		if i > 0 && strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	return n, sc.Err()
}

// snmpTCP picks OutSegs and RetransSegs from the "Tcp:" header/value line pair.
func snmpTCP(path string) (outSegs, retrans uint64) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	var head []string
	for line := range strings.SplitSeq(string(b), "\n") {
		if !strings.HasPrefix(line, "Tcp:") {
			continue
		}
		fs := strings.Fields(line)
		if head == nil {
			head = fs
			continue
		}
		for i := 1; i < len(fs) && i < len(head); i++ {
			v, _ := strconv.ParseUint(fs[i], 10, 64)
			switch head[i] {
			case "OutSegs":
				outSegs = v
			case "RetransSegs":
				retrans = v
			}
		}
		break
	}
	return outSegs, retrans
}
