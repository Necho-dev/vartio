package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"runtime"
	"sort"
	"strings"

	"govmomi-wmks/internal/guest"
)

// deviceID fingerprints the machine from the OS install id, the hardware UUID and
// the primary NIC. VM clones often share the first two; ESXi regenerates the MAC,
// so the triple still tells them apart. CPU, disks and hostname are left out: they
// are identical across clones or change on a vCPU resize / vMotion.
func deviceID() (string, *guest.DeviceFacts) {
	mid, hw, mac := norm(readMachineID()), norm(readHardwareUUID()), primaryMAC()
	facts := &guest.DeviceFacts{MID: digest(mid), HW: digest(hw), MAC: digest(mac)}
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("GUEST_DEVICE_ID"))); v != "" {
		return v, facts
	}
	if mid == "" && hw == "" && mac == "" {
		return "", nil
	}
	sum := sha256.Sum256([]byte("pd1|" + runtime.GOOS + "|" + mid + "|" + hw + "|" + mac))
	return hex.EncodeToString(sum[:10]), facts
}

func norm(s string) string {
	return strings.NewReplacer("-", "", ":", "", "{", "", "}", "").Replace(strings.ToLower(strings.TrimSpace(s)))
}

func digest(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// Interface names of bridges, tunnels and hypervisor host adapters. A guest's own
// vNIC (VMware 00:50:56 included) is a real NIC here, unlike in license fingerprints.
var virtualPrefixes = []string{"docker", "veth", "br-", "virbr", "vmnet", "vboxnet", "tun", "tap", "utun",
	"awdl", "llw", "bridge", "zt", "wg", "anpi"}
var virtualWords = []string{"virtualbox", "vethernet", "hyper-v", "vmware network adapter", "tailscale",
	"bluetooth", "npcap", "loopback", "wireguard", "zerotier", "openvpn", "wintun"}

// primaryMAC picks the smallest MAC among physical-looking NICs, so the choice does
// not depend on enumeration order or link state.
func primaryMAC() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var real, rest []string
	for _, ifc := range ifaces {
		hw := ifc.HardwareAddr
		if ifc.Flags&net.FlagLoopback != 0 || len(hw) != 6 || bytes.Equal(hw, make([]byte, 6)) || hw[0]&1 != 0 {
			continue
		}
		mac := norm(hw.String())
		if hw[0]&2 != 0 || virtualName(ifc.Name) {
			rest = append(rest, mac)
			continue
		}
		real = append(real, mac)
	}
	if len(real) == 0 {
		real = rest
	}
	if len(real) == 0 {
		return ""
	}
	sort.Strings(real)
	return real[0]
}

func virtualName(name string) bool {
	n := strings.ToLower(name)
	for _, w := range virtualWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}
