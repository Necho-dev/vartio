package agent

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// saveEnvFile writes this process's agent settings next to exe, because login
// items start without the caller's environment. An existing env file is left
// alone; returns the path when it wrote one.
func saveEnvFile(exe string) (string, error) {
	dir := filepath.Dir(exe)
	for _, name := range []string{"vartio-agent.env", "probe-agent.env"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return "", nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	p := filepath.Join(dir, "vartio-agent.env")
	var lines []string
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || (!strings.HasPrefix(k, "VARTIO_AGENT_") && !strings.HasPrefix(k, "GUEST_")) {
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
