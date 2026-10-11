//go:build windows

package agent

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// firmware reads SMBIOS strings from the registry. Win32_ComputerSystem's
// HypervisorPresent bit is also set on a physical Hyper-V host, so the
// manufacturer and product name are what distinguish a VMware guest.
func firmware() (vendor, product string) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", ""
	}
	defer k.Close()
	vendor, _, _ = k.GetStringValue("SystemManufacturer")
	product, _, _ = k.GetStringValue("SystemProductName")
	return strings.TrimSpace(vendor), strings.TrimSpace(product)
}
