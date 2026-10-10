package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The agent can run in a container (deploy/docker). Host-wide views then come
// from mounts and shared namespaces: pid: host and network_mode: host for
// processes, NICs and sockets; the host's / mounted at GUEST_HOST_ROOT for
// disks, os-release and machine-id.

var inContainer = sync.OnceValue(func() bool {
	if os.Getenv("PROBE_AGENT_RUNNER") == "docker" {
		return true
	}
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
})

// hostRoot is GUEST_HOST_ROOT, where the host's / is mounted ("" when unset).
func hostRoot() string {
	r := strings.TrimSpace(os.Getenv("GUEST_HOST_ROOT"))
	if r == "" || r == "/" {
		return ""
	}
	return filepath.Clean(r)
}

// hostPath maps a host path (a mount point) to where the agent can reach it.
func hostPath(p string) string {
	if r := hostRoot(); r != "" {
		return filepath.Join(r, p)
	}
	return p
}

// applyHostRoot points gopsutil's os-release / machine-id reads at the host.
func applyHostRoot() {
	if r := hostRoot(); r != "" && os.Getenv("HOST_ETC") == "" {
		_ = os.Setenv("HOST_ETC", filepath.Join(r, "etc"))
	}
}

// hostNS (GUEST_HOST_NS=1; needs pid: host and privileged) runs terminals and
// commands in the host's namespaces instead of the agent container's.
func hostNS() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("GUEST_HOST_NS")))
	return v == "1" || v == "true" || v == "on"
}

func hostCommand(name string, args ...string) (string, []string) {
	if !hostNS() {
		return name, args
	}
	return "nsenter", append([]string{"-t", "1", "-m", "-u", "-i", "-n", "-p", "--", name}, args...)
}

// stateDir keeps container-local identity across image upgrades; mount a volume there.
func stateDir() string {
	if d := strings.TrimSpace(os.Getenv("GUEST_STATE_DIR")); d != "" {
		return d
	}
	return "/var/lib/probe-agent"
}

// containerMachineID stands in for /etc/machine-id when the host's is not
// mounted: generated once and kept in stateDir.
func containerMachineID() string {
	path := filepath.Join(stateDir(), "machine-id")
	if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) != "" {
		return strings.TrimSpace(string(b))
	}
	id, err := randomHex(16)
	if err != nil {
		return ""
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil || os.WriteFile(path, []byte(id+"\n"), 0o644) != nil {
		return ""
	}
	return id
}
