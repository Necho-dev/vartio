package main

import (
	"os"
	"path/filepath"
	"testing"

	"govmomi-wmks/internal/guest"
)

func TestProcTCP(t *testing.T) {
	dir := t.TempDir()
	snmp := filepath.Join(dir, "snmp")
	_ = os.WriteFile(snmp, []byte("Ip: Forwarding\nIp: 1\nTcp: RtoAlgorithm OutSegs RetransSegs\nTcp: 1 500 7\n"), 0o644)
	if o, r := snmpTCP(snmp); o != 500 || r != 7 {
		t.Fatalf("snmp = %d %d", o, r)
	}
	tcp := filepath.Join(dir, "tcp")
	_ = os.WriteFile(tcp, []byte("  sl  local_address rem_address   st\n"+
		"   0: 00000000:0016 00000000:0000 0A x\n"+
		"   1: 0100007F:1F90 0100007F:D431 01 x\n"+
		"   2: 0100007F:1F90 0100007F:D432 06 x\n"), 0o644)
	var st guest.TCPStats
	if err := countTCPStates(tcp, &st); err != nil || st.Listen != 1 || st.Established != 1 || st.TimeWait != 1 || st.Total != 3 {
		t.Fatalf("states %+v %v", st, err)
	}
	udp := filepath.Join(dir, "udp")
	_ = os.WriteFile(udp, []byte("  sl  local_address\n   0: 00000000:0044\n   1: 00000000:14E9\n"), 0o644)
	if n := countLines(udp, filepath.Join(dir, "missing")); n != 2 {
		t.Fatalf("udp rows = %d", n)
	}
}
