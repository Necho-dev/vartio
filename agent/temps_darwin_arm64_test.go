//go:build darwin && arm64

package agent

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

// Several sensor names are exactly 8 bytes. A reader that writes the C
// terminator past the buffer zeroes the first byte of the next 8-byte object.
func TestSensorTempsStayInsideTheirBuffers(t *testing.T) {
	if testing.Short() {
		t.Skip("reads every sensor 100 times")
	}
	runtime.GC()
	var canaries [][]byte
	for _, n := range []int{8, 16, 24, 32, 48} {
		for range 4000 {
			canaries = append(canaries, []byte(strings.Repeat("Z", n)))
		}
	}
	got := 0
	for range 100 {
		ts, err := sensorTemps(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		got = len(ts)
	}
	for _, b := range canaries {
		if b[0] != 'Z' {
			t.Fatalf("a %d-byte object lost its first byte", len(b))
		}
	}
	if got == 0 {
		t.Fatal("no temperature sensors read")
	}
}
