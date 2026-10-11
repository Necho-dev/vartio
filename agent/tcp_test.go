package agent

import (
	"context"
	"runtime"
	"testing"
)

func TestTCPStats(t *testing.T) {
	st, err := tcpStats(context.Background())
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("unsupported platform")
	}
	if err != nil {
		t.Fatal(err)
	}
	if st.OutSegs == 0 || st.Retrans > st.OutSegs {
		t.Fatalf("segment counters %+v", st)
	}
	if st.Total < st.Established+st.TimeWait+st.Listen || st.UDP == 0 {
		t.Fatalf("socket counts %+v", st)
	}
}
