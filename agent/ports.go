package agent

import (
	"encoding/binary"
	"encoding/hex"
	"net"
	"strconv"
	"strings"

	"vartio/internal/entities"
)

func dedupePorts(in []entities.PortInfo) []entities.PortInfo {
	if len(in) == 0 {
		return []entities.PortInfo{}
	}
	seen := make(map[string]bool, len(in))
	out := make([]entities.PortInfo, 0, len(in))
	for _, p := range in {
		if p.Port <= 0 || (p.Proto != "tcp" && p.Proto != "udp") {
			continue
		}
		k := p.Proto + " " + p.IP + " " + strconv.Itoa(p.Port) + " " + strconv.FormatInt(int64(p.PID), 10)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, p)
	}
	return out
}

// parseLsofPorts reads lsof -FPcn output. A name of *:* is a socket with no
// local port and is dropped. "*" as the host is every interface.
func parseLsofPorts(b []byte) []entities.PortInfo {
	var out []entities.PortInfo
	var pid int32
	var name, proto string
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		v := unescapeLsof(line[1:])
		switch line[0] {
		case 'p':
			n, _ := strconv.Atoi(v)
			pid, name, proto = int32(n), "", ""
		case 'c':
			name = v
		case 'P':
			proto = strings.ToLower(v)
		case 'n':
			if i := strings.Index(v, "->"); i >= 0 {
				v = v[:i]
			}
			host, port, err := net.SplitHostPort(v)
			if err != nil || port == "*" {
				continue
			}
			p, err := strconv.Atoi(port)
			if err != nil {
				continue
			}
			if host == "" {
				host = "*"
			}
			out = append(out, entities.PortInfo{Proto: proto, IP: host, Port: p, PID: pid, Name: name})
		}
	}
	return dedupePorts(out)
}

// unescapeLsof reverses lsof -F escapes. Bytes outside printable ASCII, and
// backslash, are written as \xNN; a newline is \n. Chinese process names
// arrive as the UTF-8 bytes of that form.
func unescapeLsof(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
			i++
		case 'x':
			if i+3 >= len(s) {
				b.WriteByte(s[i])
				continue
			}
			v, err := strconv.ParseUint(s[i+2:i+4], 16, 8)
			if err != nil {
				b.WriteByte(s[i])
				continue
			}
			b.WriteByte(byte(v))
			i += 3
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// parseProcNet reads one /proc/net/tcp or udp table. TCP keeps state 0A (LISTEN).
// owners maps a socket inode to a pid; names maps that pid to a process name.
func parseProcNet(text, proto string, owners map[uint64]int32, names map[int32]string) []entities.PortInfo {
	var out []entities.PortInfo
	for i, line := range strings.Split(text, "\n") {
		if i == 0 {
			continue
		}
		fs := strings.Fields(line)
		if len(fs) < 10 {
			continue
		}
		if proto == "tcp" && fs[3] != "0A" {
			continue
		}
		ip, port, ok := parseProcAddr(fs[1])
		if !ok || port <= 0 {
			continue
		}
		inode, err := strconv.ParseUint(fs[9], 10, 64)
		if err != nil {
			continue
		}
		item := entities.PortInfo{Proto: proto, IP: ip, Port: port}
		if pid, ok := owners[inode]; ok && pid > 0 {
			item.PID = pid
			item.Name = names[pid]
		}
		out = append(out, item)
	}
	return out
}

func parseProcAddr(s string) (string, int, bool) {
	host, ps, ok := strings.Cut(s, ":")
	if !ok {
		return "", 0, false
	}
	port, err := strconv.ParseUint(ps, 16, 16)
	if err != nil {
		return "", 0, false
	}
	raw, err := hex.DecodeString(host)
	if err != nil {
		return "", 0, false
	}
	switch len(raw) {
	case 4:
		return net.IPv4(raw[3], raw[2], raw[1], raw[0]).String(), int(port), true
	case 16:
		for i := 0; i < 16; i += 4 {
			raw[i], raw[i+3] = raw[i+3], raw[i]
			raw[i+1], raw[i+2] = raw[i+2], raw[i+1]
		}
		return net.IP(raw).String(), int(port), true
	default:
		return "", 0, false
	}
}

// winPort decodes a MIB port DWORD (network byte order in the low 16 bits).
func winPort(v uint32) int {
	p := uint16(v)
	return int(p<<8 | p>>8)
}

func winIPv4(v uint32) string {
	return net.IPv4(byte(v), byte(v>>8), byte(v>>16), byte(v>>24)).String()
}

// parseWinTCP4 keeps LISTEN rows from a GetExtendedTcpTable buffer (AF_INET,
// TCP_TABLE_OWNER_PID_ALL). Each row is 24 bytes after the entry count.
func parseWinTCP4(b []byte) []entities.PortInfo {
	return parseWinRows(b, 24, func(row []byte) (entities.PortInfo, bool) {
		if binary.LittleEndian.Uint32(row[0:4]) != 2 {
			return entities.PortInfo{}, false
		}
		return entities.PortInfo{
			Proto: "tcp",
			IP:    winIPv4(binary.LittleEndian.Uint32(row[4:8])),
			Port:  winPort(binary.LittleEndian.Uint32(row[8:12])),
			PID:   int32(binary.LittleEndian.Uint32(row[20:24])),
		}, true
	})
}

// parseWinUDP4 reads MIB_UDPROW_OWNER_PID rows (12 bytes).
func parseWinUDP4(b []byte) []entities.PortInfo {
	return parseWinRows(b, 12, func(row []byte) (entities.PortInfo, bool) {
		return entities.PortInfo{
			Proto: "udp",
			IP:    winIPv4(binary.LittleEndian.Uint32(row[0:4])),
			Port:  winPort(binary.LittleEndian.Uint32(row[4:8])),
			PID:   int32(binary.LittleEndian.Uint32(row[8:12])),
		}, true
	})
}

// parseWinTCP6 keeps LISTEN rows. MIB_TCP6ROW_OWNER_PID is 56 bytes;
// the state DWORD sits at offset 48.
func parseWinTCP6(b []byte) []entities.PortInfo {
	return parseWinRows(b, 56, func(row []byte) (entities.PortInfo, bool) {
		if binary.LittleEndian.Uint32(row[48:52]) != 2 {
			return entities.PortInfo{}, false
		}
		return entities.PortInfo{
			Proto: "tcp",
			IP:    net.IP(row[0:16]).String(),
			Port:  winPort(binary.LittleEndian.Uint32(row[20:24])),
			PID:   int32(binary.LittleEndian.Uint32(row[52:56])),
		}, true
	})
}

// parseWinUDP6 reads MIB_UDP6ROW_OWNER_PID rows (28 bytes).
func parseWinUDP6(b []byte) []entities.PortInfo {
	return parseWinRows(b, 28, func(row []byte) (entities.PortInfo, bool) {
		return entities.PortInfo{
			Proto: "udp",
			IP:    net.IP(row[0:16]).String(),
			Port:  winPort(binary.LittleEndian.Uint32(row[20:24])),
			PID:   int32(binary.LittleEndian.Uint32(row[24:28])),
		}, true
	})
}

func parseWinRows(b []byte, row int, one func([]byte) (entities.PortInfo, bool)) []entities.PortInfo {
	if len(b) < 4 {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(b))
	var out []entities.PortInfo
	for i := range n {
		off := 4 + i*row
		if off+row > len(b) {
			break
		}
		if p, ok := one(b[off : off+row]); ok {
			out = append(out, p)
		}
	}
	return out
}
