package agent

import (
	"encoding/binary"
	"testing"
)

func TestParseLsofPorts(t *testing.T) {
	b := []byte("p10\ncmysqld\nf4\nPTCP\nn127.0.0.1:3306\nf5\nPUDP\nn*:53\nf6\nPUDP\nn*:*\nf7\nPTCP\nn127.0.0.1:3306\n")
	got := parseLsofPorts(b)
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	if got[0].Proto != "tcp" || got[0].IP != "127.0.0.1" || got[0].Port != 3306 || got[0].Name != "mysqld" || got[0].PID != 10 {
		t.Fatalf("tcp %+v", got[0])
	}
	if got[1].Proto != "udp" || got[1].IP != "*" || got[1].Port != 53 {
		t.Fatalf("udp %+v", got[1])
	}
}

func TestParseLsofPortsUTF8(t *testing.T) {
	b := []byte("p54345\nc" + `\xe6\xaf\x94\xe7\x89\xb9\xe6\xb5\x8f\xe8\xa7\x88\xe5\x99\xa8` + "\nPTCP\nn*:9222\np7\ncCursor" + `\x20Helper` + "\nPTCP\nn127.0.0.1:9229\n")
	got := parseLsofPorts(b)
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	if got[0].Name != "比特浏览器" || got[0].Port != 9222 {
		t.Fatalf("name %+v", got[0])
	}
	if got[1].Name != "Cursor Helper" || got[1].Port != 9229 {
		t.Fatalf("space %+v", got[1])
	}
}

func TestParseProcNet(t *testing.T) {
	tcp := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:0CEA 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 00000000 100 0 0 10 0\n" +
		"   1: 00000000:01BB 0100007F:C000 01 00000000:00000000 00:00000000 00000000     0        0 99 1 00000000 100 0 0 10 0\n"
	udp := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 77 1 00000000 100 0 0 10 0\n"
	owners := map[uint64]int32{12345: 10, 77: 11}
	names := map[int32]string{10: "mysqld", 11: "dnsmasq"}
	got := parseProcNet(tcp, "tcp", owners, names)
	if len(got) != 1 || got[0].Port != 3306 || got[0].IP != "127.0.0.1" || got[0].Name != "mysqld" {
		t.Fatalf("tcp %+v", got)
	}
	got = parseProcNet(udp, "udp", owners, names)
	if len(got) != 1 || got[0].Port != 53 || got[0].IP != "0.0.0.0" || got[0].Name != "dnsmasq" {
		t.Fatalf("udp %+v", got)
	}
}

func TestParseWinTables(t *testing.T) {
	// Port DWORD is network order in the low 16 bits: 3306 (0x0CEA) is stored as 0xEA0C.
	tcp := make([]byte, 4+24)
	binary.LittleEndian.PutUint32(tcp[0:], 1)
	binary.LittleEndian.PutUint32(tcp[4:], 2)          // LISTEN
	binary.LittleEndian.PutUint32(tcp[8:], 0x0100007F) // 127.0.0.1
	binary.LittleEndian.PutUint32(tcp[12:], 0xEA0C)
	binary.LittleEndian.PutUint32(tcp[24:], 10)
	got := parseWinTCP4(tcp)
	if len(got) != 1 || got[0].Port != 3306 || got[0].IP != "127.0.0.1" || got[0].PID != 10 || got[0].Proto != "tcp" {
		t.Fatalf("tcp4 %+v", got)
	}
	udp := make([]byte, 4+12)
	binary.LittleEndian.PutUint32(udp[0:], 1)
	binary.LittleEndian.PutUint32(udp[4:], 0) // 0.0.0.0
	binary.LittleEndian.PutUint32(udp[8:], 0x3500)
	binary.LittleEndian.PutUint32(udp[12:], 11)
	got = parseWinUDP4(udp)
	if len(got) != 1 || got[0].Port != 53 || got[0].IP != "0.0.0.0" || got[0].PID != 11 {
		t.Fatalf("udp4 %+v", got)
	}
	tcp6 := make([]byte, 4+56)
	binary.LittleEndian.PutUint32(tcp6[0:], 1)
	tcp6[4] = 0                                        // ::
	binary.LittleEndian.PutUint32(tcp6[4+20:], 0x5000) // port 80
	binary.LittleEndian.PutUint32(tcp6[4+48:], 2)
	binary.LittleEndian.PutUint32(tcp6[4+52:], 12)
	got = parseWinTCP6(tcp6)
	if len(got) != 1 || got[0].Port != 80 || got[0].IP != "::" || got[0].PID != 12 {
		t.Fatalf("tcp6 %+v", got)
	}
}
