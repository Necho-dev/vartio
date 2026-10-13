package agent

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"vartio/internal/entities"
)

func TestScrubSysDropsNULWithoutReread(t *testing.T) {
	got := scrubSys(entities.SysInfo{Platform: "\x00arwin", CPUModel: "Apple M1 Pro", Product: "MacBookPro18,3"})
	if got.Platform != "arwin" || bytes.IndexByte([]byte(got.CPUModel), 0) >= 0 || got.Product != "MacBookPro18,3" {
		t.Fatalf("%+v", got)
	}
}

func TestIdentityStaleAfterQuietPeriod(t *testing.T) {
	now := time.Now()
	if !identityStale(time.Time{}, now) {
		t.Fatal("empty cache should be read")
	}
	if identityStale(now, now.Add(time.Minute)) {
		t.Fatal("a fresh read should be kept")
	}
	if !identityStale(now, now.Add(identityEvery)) {
		t.Fatal("identity should refresh once the interval has passed")
	}
}

func TestEncodedReplyRejectsRawNUL(t *testing.T) {
	good, err := json.Marshal(map[string]string{"platform": "darwin"})
	if err != nil || !json.Valid(good) {
		t.Fatal(err)
	}
	bad := []byte(`{"platform":"` + "\x00arwin" + `"}`)
	if json.Valid(bad) {
		t.Fatal("raw NUL must not pass as a reply payload")
	}
}
