package guest

import "strings"

// CapacityMount names the system disk (the "系统" label), not the headline
// percentage. preferred is kept when it is writable. A read-only image — an
// erofs initrd, ISO, or Docker Desktop's /oldroot — is not the system disk
// while a writable disk exists; the largest of those wins. With only images
// left, the largest image is used.
func CapacityMount(disks []DiskUsage, preferred string) string {
	if d, ok := findDisk(disks, preferred); ok && !readOnlyImage(d) {
		return d.Mount
	}
	if m := largestDisk(disks, true); m != "" {
		return m
	}
	return largestDisk(disks, false)
}

// DiskSum is used and total across every partition. Callers pass a
// pool-deduped list so one APFS container or one device is not counted twice.
func DiskSum(disks []DiskUsage) (used, total uint64) {
	for _, d := range disks {
		if d.Total == 0 {
			continue
		}
		used += d.Used
		total += d.Total
	}
	return used, total
}

func findDisk(disks []DiskUsage, mount string) (DiskUsage, bool) {
	for _, d := range disks {
		if mount != "" && strings.EqualFold(d.Mount, mount) {
			return d, true
		}
	}
	return DiskUsage{}, false
}

// readOnlyImage is a filesystem that is full because it is an image, not
// because data filled it.
func readOnlyImage(d DiskUsage) bool {
	switch strings.ToLower(d.FS) {
	case "erofs", "iso9660", "udf", "squashfs":
		return true
	}
	m := strings.TrimRight(strings.ToLower(d.Mount), `/\`)
	return m == "/oldroot" || strings.HasPrefix(strings.ToLower(d.Mount), "/oldroot/")
}

// largestDisk picks the biggest disk. skipImage drops read-only images.
// Equal sizes keep the shorter mount, which is the broader volume.
func largestDisk(disks []DiskUsage, skipImage bool) string {
	var best *DiskUsage
	for i := range disks {
		d := &disks[i]
		if d.Total == 0 || (skipImage && readOnlyImage(*d)) {
			continue
		}
		if best == nil || d.Total > best.Total || (d.Total == best.Total && len(d.Mount) < len(best.Mount)) {
			best = d
		}
	}
	if best == nil {
		return ""
	}
	return best.Mount
}
