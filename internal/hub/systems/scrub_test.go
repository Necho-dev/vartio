package systems

import (
	"testing"

	"vartio/internal/entities"
)

func TestScrubDropsNull(t *testing.T) {
	if scrub("darwin") != "darwin" {
		t.Fatal("clean string changed")
	}
	if scrub("\x00arwin") != "arwin" {
		t.Fatal("nul not dropped")
	}
	info := entities.SysInfo{Platform: "\x00arwin", CPUModel: "\x00pple M1 Pro", Product: "MacBookPro18,3"}
	scrubInfo(&info)
	if info.Platform != "arwin" || info.CPUModel != "pple M1 Pro" || info.Product != "MacBookPro18,3" {
		t.Fatalf("%+v", info)
	}
}
