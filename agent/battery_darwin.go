//go:build darwin

package agent

import (
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"vartio/internal/entities"
)

// cfBoolSyms are loaded apart from the GPU symbols so a missing boolean
// export cannot take the graphics path down with it.
var cfBoolOnce struct {
	sync.Once
	err        error
	typeID     uintptr
	getValue   uintptr
	boolTypeID uintptr
}

func cfBoolOpen() error {
	cfBoolOnce.Do(func() {
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_LAZY)
		if err != nil {
			cfBoolOnce.err = err
			return
		}
		cfBoolOnce.typeID, err = purego.Dlsym(cf, "CFBooleanGetTypeID")
		if err != nil {
			cfBoolOnce.err = err
			return
		}
		cfBoolOnce.getValue, err = purego.Dlsym(cf, "CFBooleanGetValue")
		if err != nil {
			cfBoolOnce.err = err
			return
		}
		id, _, _ := purego.SyscallN(cfBoolOnce.typeID)
		cfBoolOnce.boolTypeID = id
	})
	return cfBoolOnce.err
}

func readBattery() *entities.Battery {
	if iokitOpen() != nil || cfBoolOpen() != nil {
		return nil
	}
	s := &iokitOnce.sym
	name := append([]byte("AppleSmartBattery"), 0)
	matching, _, _ := purego.SyscallN(s.matching, uintptr(unsafe.Pointer(&name[0])))
	runtime.KeepAlive(name)
	if matching == 0 {
		return nil
	}
	var iter uintptr
	if r, _, _ := purego.SyscallN(s.getServices, 0, matching, uintptr(unsafe.Pointer(&iter))); r != 0 || iter == 0 {
		return nil
	}
	defer purego.SyscallN(s.releaseIO, iter)
	entry, _, _ := purego.SyscallN(s.next, iter)
	if entry == 0 {
		return nil
	}
	defer purego.SyscallN(s.releaseIO, entry)
	var props uintptr
	r, _, _ := purego.SyscallN(s.props, entry, uintptr(unsafe.Pointer(&props)), 0, 0)
	if r != 0 || props == 0 {
		return nil
	}
	defer purego.SyscallN(s.cfRelease, props)
	return batteryFromApple(props)
}

func batteryFromApple(props uintptr) *entities.Battery {
	if installed, ok := cfBool(cfLookup(props, "BatteryInstalled")); ok && !installed {
		return nil
	}
	cur, okC := cfLong(cfLookup(props, "CurrentCapacity"))
	max, okM := cfLong(cfLookup(props, "MaxCapacity"))
	pct, ok := batteryPercent(cur, max)
	if !okC || !okM || !ok {
		return nil
	}
	charging, _ := cfBool(cfLookup(props, "IsCharging"))
	full, _ := cfBool(cfLookup(props, "FullyCharged"))
	external, hasExt := cfBool(cfLookup(props, "ExternalConnected"))
	return &entities.Battery{Percent: pct, State: batteryState(charging, full, hasExt && external)}
}

func cfBool(v uintptr) (bool, bool) {
	if v == 0 {
		return false, false
	}
	if n, ok := cfLong(v); ok {
		return n != 0, true
	}
	s := &iokitOnce.sym
	id, _, _ := purego.SyscallN(s.cfType, v)
	if id != cfBoolOnce.boolTypeID {
		return false, false
	}
	b, _, _ := purego.SyscallN(cfBoolOnce.getValue, v)
	return b != 0, true
}
