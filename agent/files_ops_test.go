package main

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"govmomi-wmks/internal/guest"
)

func op(t *testing.T, req guest.FilesOpRequest) (guest.FilesOpResult, *guest.FrameError) {
	t.Helper()
	b, _ := json.Marshal(req)
	return filesOp(b)
}

func TestUniqueName(t *testing.T) {
	taken := map[string]bool{"a.txt": true, "a 2.txt": true, ".env": true, "dir": true}
	has := func(n string) bool { return taken[n] }
	for in, want := range map[string]string{"a.txt": "a 3.txt", ".env": ".env 2", "dir": "dir 2", "b.txt": "b.txt"} {
		if got := uniqueName(in, has); got != want {
			t.Errorf("uniqueName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFilesOps(t *testing.T) {
	stage := t.TempDir()
	t.Setenv("GUEST_FILE_STAGING", stage)
	work := filepath.Join(t.TempDir(), "work")

	if _, fe := op(t, guest.FilesOpRequest{Op: guest.FileOpMkdir, Path: filepath.Join(work, "a", "b")}); fe != nil {
		t.Fatal(fe)
	}
	if err := os.WriteFile(filepath.Join(work, "a", "b", "x.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, fe := op(t, guest.FilesOpRequest{Op: guest.FileOpRename, Path: filepath.Join(work, "a"), To: "c"})
	if fe != nil || r.Path != filepath.Join(work, "c") {
		t.Fatalf("rename %+v %v", r, fe)
	}
	if err := os.Mkdir(filepath.Join(work, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, fe := op(t, guest.FilesOpRequest{Op: guest.FileOpRename, Path: filepath.Join(work, "c"), To: "d"}); fe == nil || fe.Code != "conflict" {
		t.Fatalf("rename onto existing: %v", fe)
	}

	z, fe := op(t, guest.FilesOpRequest{Op: guest.FileOpZip, Path: filepath.Join(work, "c")})
	if fe != nil || z.Count != 1 || filepath.Base(z.Path) != "c.zip" {
		t.Fatalf("zip %+v %v", z, fe)
	}
	zr, err := zip.OpenReader(z.Path)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	_ = zr.Close()
	if !names["b/x.txt"] || !names["b/"] {
		t.Fatalf("zip entries %v", names)
	}

	if _, fe := op(t, guest.FilesOpRequest{Op: guest.FileOpDelete, Path: filepath.Join(work, "c")}); fe == nil || fe.Code != "confirm_required" {
		t.Fatalf("delete non-empty without confirm: %v", fe)
	}
	if r, fe := op(t, guest.FilesOpRequest{Op: guest.FileOpDelete, Path: filepath.Join(work, "c"), Confirm: true}); fe != nil || r.Count != 3 {
		t.Fatalf("delete %+v %v", r, fe)
	}
	if _, fe := op(t, guest.FilesOpRequest{Op: guest.FileOpDelete, Path: string(filepath.Separator), Confirm: true}); fe == nil {
		t.Fatal("root delete must be refused")
	}
	home, _ := os.UserHomeDir()
	if _, fe := op(t, guest.FilesOpRequest{Op: guest.FileOpDelete, Path: "~", Confirm: true}); fe == nil {
		t.Fatalf("home %s delete must be refused", home)
	}
}

func TestDedupePools(t *testing.T) {
	apfs := guest.DiskUsage{FS: "apfs", Total: 500, Used: 400}
	sys, data, ext := apfs, apfs, guest.DiskUsage{Mount: "/Volumes/X", FS: "apfs", Total: 100, Used: 1}
	sys.Mount, data.Mount = "/", "/System/Volumes/Data"
	got := dedupePools([]guest.DiskUsage{sys, data, ext}, "/System/Volumes/Data")
	if len(got) != 2 || got[0].Mount != "/System/Volumes/Data" || got[1].Mount != "/Volumes/X" {
		t.Fatalf("dedupePools = %+v", got)
	}
}
