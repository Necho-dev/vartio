//go:build !windows

package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"vartio/internal/entities"
)

func trash(t *testing.T, req entities.TrashRequest) entities.TrashResult {
	t.Helper()
	b, _ := json.Marshal(req)
	r, fe := trashOp(b)
	if fe != nil {
		t.Fatalf("%s: %v", req.Op, fe)
	}
	return r
}

func TestTrashLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	work := t.TempDir()
	p := filepath.Join(work, "note.txt")
	for range 2 {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, fe := op(t, entities.FilesOpRequest{Op: entities.FileOpTrash, Path: p}); fe != nil {
			t.Fatal(fe)
		}
		if exists(p) {
			t.Fatal("source still exists")
		}
	}
	if err := os.MkdirAll(filepath.Join(work, "dir", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, fe := op(t, entities.FilesOpRequest{Op: entities.FileOpTrash, Path: filepath.Join(work, "dir")}); fe != nil {
		t.Fatal(fe)
	}

	l := trash(t, entities.TrashRequest{Op: entities.TrashList})
	if len(l.Items) != 3 {
		t.Fatalf("list %+v", l.Items)
	}
	var notes []entities.TrashItem
	for _, it := range l.Items {
		if it.Orig == "" || it.DeletedUtc == "" {
			t.Fatalf("missing metadata %+v", it)
		}
		if it.Name == "note.txt" {
			notes = append(notes, it)
		}
	}
	if len(notes) != 2 || notes[0].Orig != p {
		t.Fatalf("notes %+v", notes)
	}

	r := trash(t, entities.TrashRequest{Op: entities.TrashRestore, IDs: []string{notes[0].ID, notes[1].ID}})
	if r.Done != 2 || len(r.Items) != 1 || !exists(p) || !exists(filepath.Join(work, "note 2.txt")) {
		t.Fatalf("restore %+v", r)
	}

	b, _ := json.Marshal(entities.TrashRequest{Op: entities.TrashEmpty})
	if _, fe := trashOp(b); fe == nil || fe.Code != "confirm_required" {
		t.Fatalf("empty without confirm: %v", fe)
	}
	r = trash(t, entities.TrashRequest{Op: entities.TrashPurge, IDs: []string{"/etc/passwd"}, Confirm: true})
	if r.Done != 0 || len(r.Failed) != 1 || !exists("/etc/passwd") {
		t.Fatalf("purge outside trash %+v", r)
	}
	r = trash(t, entities.TrashRequest{Op: entities.TrashEmpty, Confirm: true})
	if r.Done != 1 || len(r.Items) != 0 {
		t.Fatalf("empty %+v", r)
	}
}
