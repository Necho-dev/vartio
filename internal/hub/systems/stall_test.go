package systems

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"vartio/internal/common"
)

func TestStallRecordedOnceAndRecovered(t *testing.T) {
	m := &Monitor{systems: map[string]*System{"a": {ID: "a", Online: true}}, stalls: map[string]*stall{}}
	timeout := &common.Error{Status: http.StatusGatewayTimeout, Code: "timeout", Message: "guest rpc timeout"}
	start := time.Now()
	for i := 0; i < stallAfter-1; i++ {
		m.pollFailed("a", start.Add(time.Duration(i)*10*time.Second), timeout)
	}
	if m.stalls["a"].logged {
		t.Fatal("a short run of failures should not be recorded")
	}
	for i := stallAfter - 1; i < stallAfter+3; i++ {
		m.pollFailed("a", start.Add(time.Duration(i)*10*time.Second), timeout)
	}
	if st := m.stalls["a"]; !st.logged || st.misses != stallAfter+3 || !st.since.Equal(start) {
		t.Fatalf("%+v", st)
	}
	e, ok := m.recoveredLocked("a", start.Add(time.Minute))
	if !ok || e.Kind != "poll.recover" || !strings.Contains(e.Detail, "1m0s") || !strings.Contains(e.Detail, "6 次") {
		t.Fatalf("%v %+v", ok, e)
	}
	if _, ok := m.recoveredLocked("a", start.Add(2*time.Minute)); ok {
		t.Fatal("recovery must be reported once")
	}
}

func TestRepeatedSnapshotIsNotStoredAgain(t *testing.T) {
	m := &Monitor{sampleAt: map[string]int64{}}
	if m.repeated("a", 1000) || m.repeated("a", 2000) || m.repeated("b", 2000) {
		t.Fatal("new snapshots are not repeats")
	}
	if !m.repeated("a", 2000) {
		t.Fatal("the same snapshot twice is a repeat")
	}
	if m.repeated("a", 1500) {
		t.Fatal("an agent clock that stepped back is still a new snapshot")
	}
	if m.repeated("a", 0) || m.repeated("a", 0) {
		t.Fatal("legacy replies without a timestamp are never repeats")
	}
}

func TestStallIgnoresOfflineAndShortRuns(t *testing.T) {
	m := &Monitor{systems: map[string]*System{"a": {ID: "a", Online: true}}, stalls: map[string]*stall{}}
	for i := 0; i < 5; i++ {
		m.pollFailed("a", time.Now(), common.ErrOffline())
	}
	if m.stalls["a"] != nil {
		t.Fatal("an agent that is gone is reported as offline, not as a stall")
	}
	m.pollFailed("a", time.Now(), &common.Error{Code: "timeout"})
	if _, ok := m.recoveredLocked("a", time.Now()); ok {
		t.Fatal("one slow poll should recover silently")
	}
	if m.stalls["a"] != nil {
		t.Fatal("a success clears the run")
	}
}
