package agent

import (
	"testing"
	"time"
)

func TestLimitedReadReturnsPreviousWhileBusy(t *testing.T) {
	var slot limitedRead[int]
	v, ok := slot.get(time.Second, func() int { return 7 })
	if !ok || v != 7 {
		t.Fatalf("first read = %d %v", v, ok)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		slot.get(2*time.Second, func() int {
			close(started)
			<-release
			return 9
		})
		close(done)
	}()
	<-started
	t0 := time.Now()
	v, ok = slot.get(time.Second, func() int {
		t.Fatal("overlapped read should not start")
		return 3
	})
	if waited := time.Since(t0); waited > 200*time.Millisecond {
		t.Fatalf("second read waited %s", waited)
	}
	if !ok || v != 7 {
		t.Fatalf("second read = %d %v, want previous 7", v, ok)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow read did not finish")
	}
	v, ok = slot.get(time.Second, func() int { return 4 })
	if !ok || v != 4 {
		t.Fatalf("read after the slow one = %d %v", v, ok)
	}
}

func TestLimitedReadDoesNotBlockPastWait(t *testing.T) {
	var slot limitedRead[int]
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go slot.get(time.Second, func() int {
		close(started)
		<-release
		return 1
	})
	<-started
	t0 := time.Now()
	_, ok := slot.get(80*time.Millisecond, func() int { return 2 })
	if ok {
		t.Fatal("expected no finished value yet")
	}
	if waited := time.Since(t0); waited > 400*time.Millisecond {
		t.Fatalf("waited %s", waited)
	}
}
