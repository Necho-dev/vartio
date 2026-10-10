package main

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// saveEnvFile writes this process's GUEST_* settings to probe-agent.env next
// to exe, because login items start without the caller's environment. An
// existing file is left alone; returns the path when it wrote one.
func saveEnvFile(exe string) (string, error) {
	p := filepath.Join(filepath.Dir(exe), "probe-agent.env")
	if _, err := os.Stat(p); err == nil {
		return "", nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var lines []string
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(k, "GUEST_") {
			continue
		}
		v = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v)
		lines = append(lines, k+`="`+v+`"`)
	}
	if len(lines) == 0 {
		return "", nil
	}
	sort.Strings(lines)
	return p, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}
