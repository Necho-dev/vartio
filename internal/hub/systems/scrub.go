package systems

import (
	"strings"

	"vartio/internal/entities"
)

// scrub drops NUL bytes. PostgreSQL text and json reject them, and one
// corrupted field would otherwise discard the whole sample or event.
func scrub(s string) string {
	if strings.IndexByte(s, 0) < 0 {
		return s
	}
	return strings.ReplaceAll(s, "\x00", "")
}

func scrubInfo(i *entities.SysInfo) {
	i.HostName = scrub(i.HostName)
	i.OS = scrub(i.OS)
	i.Platform = scrub(i.Platform)
	i.PlatformVersion = scrub(i.PlatformVersion)
	i.Kernel = scrub(i.Kernel)
	i.Arch = scrub(i.Arch)
	i.CPUModel = scrub(i.CPUModel)
	i.Vendor = scrub(i.Vendor)
	i.Product = scrub(i.Product)
	i.Virt = scrub(i.Virt)
}

func scrubSample(s *Sample) {
	s.RootMount = scrub(s.RootMount)
	for i := range s.Disks {
		s.Disks[i].Mount = scrub(s.Disks[i].Mount)
		s.Disks[i].FS = scrub(s.Disks[i].FS)
	}
	for i := range s.Temps {
		s.Temps[i].Name = scrub(s.Temps[i].Name)
	}
	for i := range s.GPUs {
		s.GPUs[i].Name = scrub(s.GPUs[i].Name)
		s.GPUs[i].Vendor = scrub(s.GPUs[i].Vendor)
	}
	for i := range s.NICs {
		s.NICs[i].Name = scrub(s.NICs[i].Name)
		s.NICs[i].MAC = scrub(s.NICs[i].MAC)
		s.NICs[i].IPv4 = scrub(s.NICs[i].IPv4)
	}
	for i := range s.NetNICs {
		s.NetNICs[i].Name = scrub(s.NetNICs[i].Name)
	}
	if d := s.Docker; d != nil {
		d.Engine = scrub(d.Engine)
		d.Version = scrub(d.Version)
		d.Error = scrub(d.Error)
		for i := range d.Containers {
			c := &d.Containers[i]
			c.ID = scrub(c.ID)
			c.Name = scrub(c.Name)
			c.Image = scrub(c.Image)
			c.State = scrub(c.State)
			c.Status = scrub(c.Status)
			c.Health = scrub(c.Health)
			c.Project = scrub(c.Project)
			c.Service = scrub(c.Service)
			c.Compose = scrub(c.Compose)
			c.Ports = scrub(c.Ports)
		}
	}
}
