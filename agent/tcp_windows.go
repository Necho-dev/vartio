package main

import (
	"context"
	"encoding/binary"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"

	"govmomi-wmks/internal/guest"
)

var (
	iphlpapi           = windows.NewLazySystemDLL("iphlpapi.dll")
	procExtTCPTable    = iphlpapi.NewProc("GetExtendedTcpTable")
	procExtUDPTable    = iphlpapi.NewProc("GetExtendedUdpTable")
	procTCPStatisticEx = iphlpapi.NewProc("GetTcpStatisticsEx")
)

const (
	tcpTableOwnerPIDAll = 5
	udpTableOwnerPID    = 1
	tcpListen           = 2
	tcpEstablished      = 5
	tcpTimeWait         = 11
)

// tcpStats reads the IPv4 and IPv6 TCP tables and statistics from iphlpapi;
// gopsutil's connection list also resolves every owning process.
func tcpStats(context.Context) (*guest.TCPStats, error) {
	out := &guest.TCPStats{}
	ok := false
	// Row layouts: MIB_TCPROW_OWNER_PID (state first, 24 bytes) and
	// MIB_TCP6ROW_OWNER_PID (state at 48, 56 bytes).
	for _, t := range []struct {
		af            uint32
		size, stateAt int
	}{{windows.AF_INET, 24, 0}, {windows.AF_INET6, 56, 48}} {
		b, err := extTable(procExtTCPTable, t.af, tcpTableOwnerPIDAll)
		if err != nil || len(b) < 4 {
			continue
		}
		ok = true
		n := int(binary.LittleEndian.Uint32(b))
		out.Total += n
		if u, err := extTable(procExtUDPTable, t.af, udpTableOwnerPID); err == nil && len(u) >= 4 {
			out.UDP += int(binary.LittleEndian.Uint32(u))
		}
		for i := range n {
			off := 4 + i*t.size + t.stateAt
			if off+4 > len(b) {
				break
			}
			switch binary.LittleEndian.Uint32(b[off:]) {
			case tcpEstablished:
				out.Established++
			case tcpTimeWait:
				out.TimeWait++
			case tcpListen:
				out.Listen++
			}
		}
		// MIB_TCPSTATS: dwOutSegs and dwRetransSegs are the 11th and 12th DWORDs.
		// dwOutSegs excludes retransmissions (RFC 4022), unlike Linux's OutSegs
		// and nettop's bytes_out, so add them back to report total segments sent.
		var st [15]uint32
		if r, _, _ := procTCPStatisticEx.Call(uintptr(unsafe.Pointer(&st[0])), uintptr(t.af)); r == 0 {
			out.OutSegs += uint64(st[10]) + uint64(st[11])
			out.Retrans += uint64(st[11])
		}
	}
	if !ok {
		return nil, errors.New("GetExtendedTcpTable failed")
	}
	return out, nil
}

// extTable calls GetExtendedTcpTable / GetExtendedUdpTable (same signature),
// growing the buffer while the table is changing size.
func extTable(proc *windows.LazyProc, af, class uint32) ([]byte, error) {
	var size uint32
	for range 4 {
		var buf []byte
		var p uintptr
		if size > 0 {
			buf = make([]byte, size)
			p = uintptr(unsafe.Pointer(&buf[0]))
		}
		r, _, _ := proc.Call(p, uintptr(unsafe.Pointer(&size)), 0, uintptr(af), uintptr(class), 0)
		switch windows.Errno(r) {
		case 0:
			return buf[:size], nil
		case windows.ERROR_INSUFFICIENT_BUFFER:
			size += 4096
			continue
		default:
			return nil, windows.Errno(r)
		}
	}
	return nil, windows.ERROR_INSUFFICIENT_BUFFER
}
