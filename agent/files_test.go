package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveGuestPathStaging(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GUEST_FILE_STAGING", dir)

	abs, err := resolveGuestPath("a/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if abs != filepath.Join(dir, "a", "b.txt") {
		t.Fatalf("got %s", abs)
	}
	_, err = resolveGuestPath("../escape")
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("want escape error, got %v", err)
	}
	root, err := resolveGuestPath("")
	if err != nil || root != dir {
		t.Fatalf("empty path %s %v", root, err)
	}
}

func TestFilesPutGetList(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GUEST_FILE_STAGING", dir)
	name := "hello.txt"
	put, err := filesPut(mustJSON(t, guestPut(name, 0, []byte("hi"), true, true)))
	if err != nil || !put.OK || put.SHA256 == "" {
		t.Fatalf("put %+v %v", put, err)
	}
	got, err := filesGet(mustJSON(t, map[string]any{"path": name, "offset": 0, "length": 16}))
	if err != nil || string(got.Data) != "hi" || !got.EOF {
		t.Fatalf("get %+v %v", got, err)
	}
	list, err := filesList(mustJSON(t, map[string]any{"path": ""}))
	if err != nil || len(list.Entries) != 1 || list.Entries[0].Name != name {
		t.Fatalf("list %+v %v", list, err)
	}
	_, err = filesPut(mustJSON(t, guestPut(name, 0, []byte("no"), true, false)))
	if err == nil || err.Code != "confirm_required" {
		t.Fatalf("overwrite without confirm: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
		t.Fatal(statErr)
	}
}

func guestPut(path string, offset int64, data []byte, eof, confirm bool) map[string]any {
	return map[string]any{
		"path": path, "offset": offset, "data": data, "eof": eof, "confirm": confirm,
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
