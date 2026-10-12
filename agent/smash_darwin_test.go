//go:build darwin

package agent

import (
	"strings"
	"sync"
	"testing"
)

func TestCollectorsDoNotSmashIdentity(t *testing.T) {
	before := staticInfo()
	want := map[string]string{
		"platform": before.Platform,
		"version":  before.PlatformVersion,
		"kernel":   before.Kernel,
		"cpu":      before.CPUModel,
		"product":  before.Product,
		"vendor":   before.Vendor,
	}
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 8; i++ {
				_ = collectSnapshot()
			}
		}()
	}
	wg.Wait()
	for i := 0; i < 1; i++ {
		got := staticInfo()
		now := map[string]string{
			"platform": got.Platform,
			"version":  got.PlatformVersion,
			"kernel":   got.Kernel,
			"cpu":      got.CPUModel,
			"product":  got.Product,
			"vendor":   got.Vendor,
		}
		for k, v := range now {
			if strings.IndexByte(v, 0) >= 0 || v != want[k] {
				t.Fatalf("after sample %d %s changed %q -> %q", i, k, want[k], v)
			}
		}
	}
}
