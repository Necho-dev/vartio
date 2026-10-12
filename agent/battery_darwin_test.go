//go:build darwin

package agent

import "testing"

func TestAppleBatteryLive(t *testing.T) {
	b := readBattery()
	if b == nil {
		t.Skip("no AppleSmartBattery")
	}
	if b.Percent < 0 || b.Percent > 100 {
		t.Fatalf("percent %+v", b)
	}
	switch b.State {
	case battCharging, battFull, battDischarging, battAC:
	default:
		t.Fatalf("state %+v", b)
	}
	t.Logf("battery %.1f%% %s", b.Percent, b.State)
}
