//go:build darwin

package agent

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestListAppsLive(t *testing.T) {
	apps, err := listApps()
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) < 5 {
		t.Fatalf("only %d apps", len(apps))
	}
	var named int
	for _, a := range apps {
		if a.Name == "" || a.Path == "" || !strings.HasSuffix(a.Path, ".app") {
			t.Fatalf("bad row %+v", a)
		}
		if a.Version != "" {
			named++
		}
		switch a.Arch {
		case "", "universal", "arm64", "x64", "x86":
		default:
			t.Fatalf("arch %s %+v", a.Arch, a)
		}
	}
	if named == 0 {
		t.Fatal("no versions")
	}
	for _, a := range apps {
		switch filepath.Base(a.Path) {
		case "AltTab.app":
			if a.Version == "" || a.LastUsed == "" || a.Arch == "" {
				t.Fatalf("alttab %+v", a)
			}
		case "Boop.app":
			if a.Source != "appstore" {
				t.Fatalf("boop %+v", a)
			}
		case "Cursor.app":
			if a.Arch != "arm64" || a.Version == "" {
				t.Fatalf("cursor %+v", a)
			}
		case "Ping Island.app", "CC Switch.app":
			if a.Source != "homebrew" {
				t.Fatalf("cask %+v", a)
			}
		}
	}
}
