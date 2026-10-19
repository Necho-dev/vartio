package systems

import (
	"testing"
	"time"
)

func TestPruneNotable(t *testing.T) {
	const per int64 = 360 // 10s samples, one host-hour
	for _, c := range []struct {
		n, prev int64
		want    bool
	}{
		{1440, 0, true},     // first hour
		{1436, 1440, false}, // a few missed polls
		{1373, 1440, false}, // under half a host and under 10%
		{1225, 1440, true},  // one host dropped out for part of the hour
		{1440, 1225, true},  // recovered
		{1451, 1121, true},  // a fourth host aged into retention
		{1080, 1058, false},
	} {
		if got := pruneNotable(c.n, c.prev, per); got != c.want {
			t.Errorf("pruneNotable(%d, %d) = %v, want %v", c.n, c.prev, got, c.want)
		}
	}
}

func TestPruneMessages(t *testing.T) {
	cutoff := time.Date(2026, 10, 12, 10, 18, 46, 0, time.Local)
	keep, every := 7*24*time.Hour, 10*time.Second
	hour := pruneHourMessage(1225, 1440, 360, cutoff, keep, every)
	want := "monitor prune: deleted 1225 raw samples (was 1440) before 2026-10-12 10:18:46 (keep 7d, ~3.4 hosts at 10s)"
	if hour != want {
		t.Errorf("hour message:\n%s\nwant\n%s", hour, want)
	}
	day := pruneDayMessage("2026-10-18", 34560, 24, 360, keep, every)
	want = "monitor prune: 2026-10-18 deleted 34560 raw samples over 24h (keep 7d, ~4 hosts at 10s)"
	if day != want {
		t.Errorf("day message:\n%s\nwant\n%s", day, want)
	}
}
