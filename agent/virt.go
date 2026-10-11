package agent

import "strings"

// virtualization reports SMBIOS vendor/product and, when those identify a
// guest, the hypervisor. A physical machine returns an empty virt.
func virtualization() (vendor, product, virt string) {
	vendor, product = firmware()
	return vendor, product, guestHypervisor(vendor, product)
}

// guestHypervisor maps firmware identity to a hypervisor. Physical vendors
// (Dell, Apple, Lenovo, …) return "".
func guestHypervisor(vendor, product string) string {
	s := strings.ToLower(vendor + " " + product)
	switch {
	case strings.Contains(s, "vmware"):
		return "vmware"
	case strings.Contains(s, "virtualbox") || strings.Contains(s, "innotek"):
		return "virtualbox"
	case strings.Contains(s, "parallels"):
		return "parallels"
	case strings.Contains(s, "qemu") || strings.Contains(s, "kvm"):
		return "kvm"
	case strings.Contains(s, "xen"):
		return "xen"
	case strings.Contains(s, "bhyve"):
		return "bhyve"
	case strings.Contains(s, "microsoft") && (strings.Contains(s, "virtual") || strings.Contains(s, "hyper-v")):
		return "hyperv"
	default:
		return ""
	}
}
