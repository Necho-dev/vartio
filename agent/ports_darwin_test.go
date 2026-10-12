//go:build darwin

package agent

import "testing"

func TestListPortsLive(t *testing.T) {
	ps, err := listPorts()
	if err != nil {
		t.Fatal(err)
	}
	var tcp, udp int
	for _, p := range ps {
		if p.Port <= 0 || p.Name == "" || p.PID <= 0 {
			t.Fatalf("%+v", p)
		}
		switch p.Proto {
		case "tcp":
			tcp++
		case "udp":
			udp++
		default:
			t.Fatalf("proto %+v", p)
		}
	}
	if tcp == 0 || udp == 0 {
		t.Fatalf("tcp=%d udp=%d of %d", tcp, udp, len(ps))
	}
}
