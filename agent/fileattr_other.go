//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
)

func hiddenAttr(os.FileInfo) bool { return false }

func fileRoots() []string {
	out := []string{"/"}
	if runtime.GOOS == "darwin" {
		vols, _ := filepath.Glob("/Volumes/*")
		for _, v := range vols {
			if st, err := os.Stat(v); err == nil && st.IsDir() {
				out = append(out, v)
			}
		}
	}
	return out
}
