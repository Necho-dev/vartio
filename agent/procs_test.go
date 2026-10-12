package agent

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"vartio/internal/entities"
)

func TestListProcs(t *testing.T) {
	start := time.Now()
	rows, err := listProcs()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 5 {
		t.Fatalf("only %d processes", len(rows))
	}
	if elapsed > 3*time.Second {
		t.Fatalf("listProcs took %s for %d processes", elapsed, len(rows))
	}
	if runtime.GOOS != "darwin" {
		return
	}
	pid := int32(os.Getpid())
	var self *entities.ProcInfo
	known := 0
	for i := range rows {
		switch rows[i].Status {
		case "running", "sleep", "stop", "zombie", "idle":
			known++
		}
		if rows[i].PID == pid {
			self = &rows[i]
		}
	}
	if self == nil || self.Name == "" || self.MemMB <= 0 {
		t.Fatalf("missing self process in %d rows", len(rows))
	}
	if strings.Contains(self.Cmdline, "-test.") {
		t.Fatalf("command line includes test arguments: %s", self.Cmdline)
	}
	if !strings.HasSuffix(self.Cmdline, "/"+self.Name) {
		t.Fatalf("name %q is not the executable %q", self.Name, self.Cmdline)
	}
	if known*2 < len(rows) {
		t.Fatalf("only %d/%d processes have a known status", known, len(rows))
	}
	t.Logf("%d processes in %s; self %s %.1f MB %s", len(rows), elapsed.Round(time.Millisecond), self.Name, self.MemMB, self.Status)
}
