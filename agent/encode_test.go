package agent

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func TestEncodeRestartsAfterRepeatedFailures(t *testing.T) {
	var restarts atomic.Int32
	old := restartSelf
	restartSelf = func() error { restarts.Add(1); return nil }
	defer func() { restartSelf = old }()

	bad := json.RawMessage("{\"a\":\x00}")
	for range encodeFailLimit - 1 {
		if _, err := encode("t.bad", bad); err == nil {
			t.Fatal("a raw NUL must not encode")
		}
	}
	if _, err := encode("t.bad", map[string]int{"ok": 1}); err != nil {
		t.Fatal(err)
	}
	for range encodeFailLimit - 1 {
		_, _ = encode("t.bad", bad)
	}
	// Failures of another method do not add up with this one.
	_, _ = encode("t.other", bad)
	time.Sleep(700 * time.Millisecond)
	if restarts.Load() != 0 {
		t.Fatal("a success in between, or another method, must reset the count")
	}
	_, _ = encode("t.bad", bad)
	time.Sleep(700 * time.Millisecond)
	if restarts.Load() != 1 {
		t.Fatalf("restarts = %d", restarts.Load())
	}
}
