package agent

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentZipsGetTheirOwnDirectory(t *testing.T) {
	t.Setenv("VARTIO_AGENT_FILE_STAGING", t.TempDir())
	src := t.TempDir()
	const n = 8
	for i := range n {
		d := filepath.Join(src, string(rune('a'+i)))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "f.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dirs := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			res, ferr := fileZip(filepath.Join(src, string(rune('a'+i))))
			if ferr != nil {
				t.Error(ferr.Message)
				return
			}
			dirs[i] = filepath.Dir(res.Path)
		})
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, d := range dirs {
		if seen[d] {
			t.Fatalf("two zips share %s; finishing one download would delete the other", d)
		}
		seen[d] = true
	}
}
