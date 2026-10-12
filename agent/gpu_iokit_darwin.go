//go:build darwin

package agent

import (
	"bytes"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"vartio/internal/entities"
)

// Apple fills Device / Renderer / Tiler Utilization only when a client copies
// the registry properties. The ioreg command prints the stale copy, which stays
// at 0; IORegistryEntryCreateCFProperties is what menu-bar monitors call.
const (
	cfStringEncodingUTF8 = 0x08000100
	cfNumberLongLong     = 11 // kCFNumberLongLongType
)

var iokitOnce struct {
	sync.Once
	err error
	sym iokitSyms
}

type iokitSyms struct {
	matching, getServices, next, props, releaseIO uintptr
	cfString, cfGet, cfType, cfNumType, cfStrType,
	cfNumValue, cfCString, cfRelease uintptr
}

func iokitOpen() error {
	iokitOnce.Do(func() {
		io, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_LAZY)
		if err != nil {
			iokitOnce.err = err
			return
		}
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_LAZY)
		if err != nil {
			iokitOnce.err = err
			return
		}
		bind := func(h uintptr, name string, dst *uintptr) bool {
			*dst, err = purego.Dlsym(h, name)
			if err != nil {
				iokitOnce.err = err
				return false
			}
			return true
		}
		s := &iokitOnce.sym
		if !bind(io, "IOServiceMatching", &s.matching) ||
			!bind(io, "IOServiceGetMatchingServices", &s.getServices) ||
			!bind(io, "IOIteratorNext", &s.next) ||
			!bind(io, "IORegistryEntryCreateCFProperties", &s.props) ||
			!bind(io, "IOObjectRelease", &s.releaseIO) ||
			!bind(cf, "CFStringCreateWithCString", &s.cfString) ||
			!bind(cf, "CFDictionaryGetValue", &s.cfGet) ||
			!bind(cf, "CFGetTypeID", &s.cfType) ||
			!bind(cf, "CFNumberGetTypeID", &s.cfNumType) ||
			!bind(cf, "CFStringGetTypeID", &s.cfStrType) ||
			!bind(cf, "CFNumberGetValue", &s.cfNumValue) ||
			!bind(cf, "CFStringGetCString", &s.cfCString) ||
			!bind(cf, "CFRelease", &s.cfRelease) {
			return
		}
	})
	return iokitOnce.err
}

func appleGPUFromIOKit() []entities.GPUInfo {
	if iokitOpen() != nil {
		return nil
	}
	s := &iokitOnce.sym
	name := append([]byte("IOAccelerator"), 0)
	matching, _, _ := purego.SyscallN(s.matching, uintptr(unsafe.Pointer(&name[0])))
	runtime.KeepAlive(name)
	if matching == 0 {
		return nil
	}
	var iter uintptr
	// GetMatchingServices consumes the matching dictionary.
	if r, _, _ := purego.SyscallN(s.getServices, 0, matching, uintptr(unsafe.Pointer(&iter))); r != 0 || iter == 0 {
		return nil
	}
	defer purego.SyscallN(s.releaseIO, iter)

	var out []entities.GPUInfo
	for range 8 {
		entry, _, _ := purego.SyscallN(s.next, iter)
		if entry == 0 {
			break
		}
		var props uintptr
		r, _, _ := purego.SyscallN(s.props, entry, uintptr(unsafe.Pointer(&props)), 0, 0)
		purego.SyscallN(s.releaseIO, entry)
		if r != 0 || props == 0 {
			continue
		}
		if g, ok := gpuFromProperties(props); ok {
			out = append(out, g)
		}
		purego.SyscallN(s.cfRelease, props)
	}
	return out
}

func gpuFromProperties(props uintptr) (entities.GPUInfo, bool) {
	stats := cfLookup(props, "PerformanceStatistics")
	if stats == 0 {
		return entities.GPUInfo{}, false
	}
	util, ok := cfLong(cfLookup(stats, "Device Utilization %"))
	if !ok {
		return entities.GPUInfo{}, false
	}
	g := entities.GPUInfo{UtilPct: float64(util), Vendor: "apple"}
	g.Name = cfText(cfLookup(props, "model"))
	if g.Name == "" {
		g.Name = "GPU"
	}
	if render, ok := cfLong(cfLookup(stats, "Renderer Utilization %")); ok {
		v := float64(render)
		g.RenderPct = &v
	}
	if tiler, ok := cfLong(cfLookup(stats, "Tiler Utilization %")); ok {
		v := float64(tiler)
		g.TilerPct = &v
	}
	if mem, ok := cfLong(cfLookup(stats, "In use system memory")); ok && mem > 0 {
		g.MemUsed = uint64(mem)
	}
	if cores, ok := cfLong(cfLookup(props, "gpu-core-count")); ok && cores > 0 {
		g.Cores = int(cores)
	}
	return g, true
}

func cfLookup(dict uintptr, key string) uintptr {
	if dict == 0 {
		return 0
	}
	b := append([]byte(key), 0)
	s := &iokitOnce.sym
	str, _, _ := purego.SyscallN(s.cfString, 0, uintptr(unsafe.Pointer(&b[0])), cfStringEncodingUTF8)
	runtime.KeepAlive(b)
	if str == 0 {
		return 0
	}
	v, _, _ := purego.SyscallN(s.cfGet, dict, str)
	purego.SyscallN(s.cfRelease, str)
	return v
}

func cfLong(v uintptr) (int64, bool) {
	if v == 0 {
		return 0, false
	}
	s := &iokitOnce.sym
	id, _, _ := purego.SyscallN(s.cfType, v)
	num, _, _ := purego.SyscallN(s.cfNumType)
	if id != num {
		return 0, false
	}
	var n int64
	ok, _, _ := purego.SyscallN(s.cfNumValue, v, cfNumberLongLong, uintptr(unsafe.Pointer(&n)))
	return n, ok != 0
}

func cfText(v uintptr) string {
	if v == 0 {
		return ""
	}
	s := &iokitOnce.sym
	id, _, _ := purego.SyscallN(s.cfType, v)
	str, _, _ := purego.SyscallN(s.cfStrType)
	if id != str {
		return ""
	}
	buf := make([]byte, 256)
	ok, _, _ := purego.SyscallN(s.cfCString, v, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), cfStringEncodingUTF8)
	runtime.KeepAlive(buf)
	if ok == 0 {
		return ""
	}
	if i := bytes.IndexByte(buf, 0); i >= 0 {
		buf = buf[:i]
	}
	return string(buf)
}
