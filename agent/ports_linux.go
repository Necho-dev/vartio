//go:build linux

package agent

import (
	"os"
	"strconv"
	"strings"

	"vartio/internal/entities"
)

func listPorts() ([]entities.PortInfo, error) {
	owners, names := socketOwners()
	var out []entities.PortInfo
	for _, p := range []struct{ path, proto string }{
		{"/proc/net/tcp", "tcp"}, {"/proc/net/tcp6", "tcp"},
		{"/proc/net/udp", "udp"}, {"/proc/net/udp6", "udp"},
	} {
		b, err := os.ReadFile(p.path)
		if err != nil {
			continue
		}
		out = append(out, parseProcNet(string(b), p.proto, owners, names)...)
	}
	if len(out) == 0 {
		if _, err := os.Stat("/proc/net/tcp"); err != nil {
			return nil, err
		}
	}
	return dedupePorts(out), nil
}

// socketOwners maps a socket inode to one pid and that pid's comm name.
// A socket shared across a fork keeps the first pid found.
func socketOwners() (map[uint64]int32, map[int32]string) {
	owners := map[uint64]int32{}
	names := map[int32]string{}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return owners, names
	}
	for _, e := range procs {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		fds, err := os.ReadDir("/proc/" + e.Name() + "/fd")
		if err != nil {
			continue
		}
		id := int32(pid)
		for _, fd := range fds {
			target, err := os.Readlink("/proc/" + e.Name() + "/fd/" + fd.Name())
			if err != nil || !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
				continue
			}
			inode, err := strconv.ParseUint(target[len("socket:["):len(target)-1], 10, 64)
			if err != nil {
				continue
			}
			if _, ok := owners[inode]; ok {
				continue
			}
			owners[inode] = id
			if _, ok := names[id]; !ok {
				if b, err := os.ReadFile("/proc/" + e.Name() + "/comm"); err == nil {
					names[id] = strings.TrimSpace(string(b))
				}
			}
		}
	}
	return owners, names
}
