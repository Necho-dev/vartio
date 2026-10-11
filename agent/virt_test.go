package main

import "testing"

func TestGuestHypervisor(t *testing.T) {
	cases := []struct {
		vendor, product, want string
	}{
		{"VMware, Inc.", "VMware Virtual Platform", "vmware"},
		{"VMware, Inc.", "VMware20,1", "vmware"},
		{"Dell Inc.", "OptiPlex 7080", ""},
		{"LENOVO", "ThinkPad T14", ""},
		{"Apple", "iMac21,1", ""},
		{"Microsoft Corporation", "Virtual Machine", "hyperv"},
		{"QEMU", "Standard PC (Q35 + ICH9, 2009)", "kvm"},
		{"innotek GmbH", "VirtualBox", "virtualbox"},
	}
	for _, c := range cases {
		if got := guestHypervisor(c.vendor, c.product); got != c.want {
			t.Errorf("%s / %s = %q, want %q", c.vendor, c.product, got, c.want)
		}
	}
}
