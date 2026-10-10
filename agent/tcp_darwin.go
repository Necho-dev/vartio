package main

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"

	"govmomi-wmks/internal/guest"
)

// tcpStats counts sockets from netstat (a few ms); gopsutil's connection list
// shells out to lsof, which is much slower. net.inet.tcp.stats reads as zeros
// without root, so segment counters come from nettop's per-process TCP byte
// totals (OutSegs/Retrans are bytes here; the ratio is what matters). Totals
// drop when processes exit; the hub skips such intervals.
func tcpStats(ctx context.Context) (*guest.TCPStats, error) {
	out := &guest.TCPStats{}
	for _, fam := range []string{"inet", "inet6"} {
		b, err := exec.CommandContext(ctx, "/usr/sbin/netstat", "-an", "-f", fam).Output()
		if err != nil {
			return nil, err
		}
		countNetstat(b, fam == "inet6", out)
	}
	if b, err := exec.CommandContext(ctx, "/usr/bin/nettop", "-m", "tcp", "-P", "-L", "1", "-x", "-J", "bytes_out,re-tx").Output(); err == nil {
		out.OutSegs, out.Retrans = nettopTotals(b)
	}
	return out, nil
}

// countNetstat tallies `netstat -an -f inet|inet6` rows by protocol (tcp4, udp6,
// icm4, ...). Dual-stack sockets (tcp46/udp46) appear in both listings, so the
// inet6 pass skips them.
func countNetstat(b []byte, v6 bool, out *guest.TCPStats) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 5 || (v6 && strings.HasSuffix(fs[0], "46")) {
			continue
		}
		switch {
		case strings.HasPrefix(fs[0], "tcp"):
			out.Total++
			switch fs[len(fs)-1] {
			case "ESTABLISHED":
				out.Established++
			case "TIME_WAIT":
				out.TimeWait++
			case "LISTEN":
				out.Listen++
			}
		case strings.HasPrefix(fs[0], "udp"):
			out.UDP++
		case strings.HasPrefix(fs[0], "icm"), strings.HasPrefix(fs[0], "raw"):
			out.Other++
		}
	}
}

// nettopTotals sums the bytes_out and re-tx columns of nettop's CSV.
func nettopTotals(b []byte) (sent, retx uint64) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Scan() // header
	for sc.Scan() {
		fs := strings.Split(sc.Text(), ",")
		if len(fs) < 3 {
			continue
		}
		o, _ := strconv.ParseUint(fs[1], 10, 64)
		r, _ := strconv.ParseUint(fs[2], 10, 64)
		sent += o
		retx += r
	}
	return sent, retx
}
