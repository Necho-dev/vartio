package guest

import (
	"strconv"
	"strings"
)

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

// DedupePools keeps one mount per storage pool. Volumes that report the same
// filesystem, size and used space (one device mounted twice, or an APFS
// container) share that space; root wins. macOS "/" and
// "/System/Volumes/Data" are that pair even when their used counts disagree:
// the two statfs calls are not atomic, so a write between them otherwise
// looks like a disk appearing and vanishing on the next sample.
func DedupePools(disks []DiskUsage, root string) []DiskUsage {
	disks = foldSystemVolume(disks, root)
	key := func(d DiskUsage) string { return d.FS + "|" + u64(d.Total) + "|" + u64(d.Used) }
	keep := map[string]string{}
	for _, d := range disks {
		if k := key(d); keep[k] == "" || d.Mount == root {
			keep[k] = d.Mount
		}
	}
	out := make([]DiskUsage, 0, len(disks))
	for _, d := range disks {
		if keep[key(d)] == d.Mount {
			out = append(out, d)
		}
	}
	return out
}

func u64(n uint64) string {
	return strconv.FormatUint(n, 10)
}

// foldSystemVolume drops the macOS sealed system volume when Data is the same
// container, or the reverse when "/" is the chosen root.
func foldSystemVolume(disks []DiskUsage, root string) []DiskUsage {
	var sys, data *DiskUsage
	for i := range disks {
		switch disks[i].Mount {
		case "/":
			sys = &disks[i]
		case "/System/Volumes/Data":
			data = &disks[i]
		}
	}
	if sys == nil || data == nil || !sameContainer(*sys, *data) {
		return disks
	}
	drop := "/"
	if root == "/" {
		drop = "/System/Volumes/Data"
	}
	out := make([]DiskUsage, 0, len(disks)-1)
	for _, d := range disks {
		if d.Mount != drop {
			out = append(out, d)
		}
	}
	return out
}

func sameContainer(a, b DiskUsage) bool {
	if a.Total == 0 || b.Total == 0 {
		return false
	}
	if a.FS != "" && b.FS != "" && !strings.EqualFold(a.FS, b.FS) {
		return false
	}
	hi, lo := a.Total, b.Total
	if lo > hi {
		hi, lo = lo, hi
	}
	return float64(hi-lo)/float64(lo) <= 0.02
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
