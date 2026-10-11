package agent

import (
	"os"
	"strings"
)

// getenv reads the Vartio name first, then the previous probe name.
// Installed agents keep GUEST_* / PROBE_* in their env files.
func getenv(primary string, aliases ...string) string {
	for _, k := range append([]string{primary}, aliases...) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// existingFile returns the first path that is already on disk, otherwise the first candidate.
func existingFile(paths ...string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}
