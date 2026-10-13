//go:build darwin && arm64

package agent

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/shirou/gopsutil/v4/sensors"
)

// gopsutil 4.25 sizes the sensor-name buffer to the name length but tells
// CFStringGetCString it is one byte longer. When a name exactly fills its
// allocation, the terminating NUL lands on the first byte of the next heap
// object, which corrupted identity strings and the JSON encoder's cached field
// names. These sensors are read here instead, through cfText's sized buffer.

const (
	hidEventTemperature = 15 // kIOHIDEventTypeTemperature
	cfNumberSInt32      = 3  // kCFNumberSInt32Type
)

var hidTemps struct {
	sync.Once
	err error

	create       func(alloc uintptr) uintptr
	setMatching  func(client, match uintptr)
	copyServices func(client uintptr) uintptr
	copyProperty func(service, key uintptr) uintptr
	copyEvent    func(service uintptr, eventType int64, options int32, timeout int64) uintptr
	floatValue   func(event uintptr, field int32) float64
	arrayCount   func(array uintptr) int64
	arrayAt      func(array uintptr, i int64) uintptr
	numberCreate func(alloc uintptr, numberType int64, value unsafe.Pointer) uintptr
	dictCreate   func(alloc uintptr, keys, values unsafe.Pointer, n int64, keyCB, valueCB uintptr) uintptr
	keyCB, valCB uintptr
}

func hidOpen() error {
	hidTemps.Do(func() {
		if err := iokitOpen(); err != nil {
			hidTemps.err = err
			return
		}
		io, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_LAZY)
		if err != nil {
			hidTemps.err = err
			return
		}
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_LAZY)
		if err != nil {
			hidTemps.err = err
			return
		}
		h := &hidTemps
		for _, b := range []struct {
			lib  uintptr
			name string
			fn   any
		}{
			{io, "IOHIDEventSystemClientCreate", &h.create},
			{io, "IOHIDEventSystemClientSetMatching", &h.setMatching},
			{io, "IOHIDEventSystemClientCopyServices", &h.copyServices},
			{io, "IOHIDServiceClientCopyProperty", &h.copyProperty},
			{io, "IOHIDServiceClientCopyEvent", &h.copyEvent},
			{io, "IOHIDEventGetFloatValue", &h.floatValue},
			{cf, "CFArrayGetCount", &h.arrayCount},
			{cf, "CFArrayGetValueAtIndex", &h.arrayAt},
			{cf, "CFNumberCreate", &h.numberCreate},
			{cf, "CFDictionaryCreate", &h.dictCreate},
		} {
			sym, err := purego.Dlsym(b.lib, b.name)
			if err != nil {
				h.err = err
				return
			}
			purego.RegisterFunc(b.fn, sym)
		}
		if h.keyCB, err = purego.Dlsym(cf, "kCFTypeDictionaryKeyCallBacks"); err != nil {
			h.err = err
			return
		}
		if h.valCB, err = purego.Dlsym(cf, "kCFTypeDictionaryValueCallBacks"); err != nil {
			h.err = err
		}
	})
	return hidTemps.err
}

func cfCreateString(s string) uintptr {
	b := append([]byte(s), 0)
	str, _, _ := purego.SyscallN(iokitOnce.sym.cfString, 0, uintptr(unsafe.Pointer(&b[0])), cfStringEncodingUTF8)
	runtime.KeepAlive(b)
	return str
}

func cfRelease(v uintptr) {
	if v != 0 {
		purego.SyscallN(iokitOnce.sym.cfRelease, v)
	}
}

// sensorTemps reads every IOHID temperature sensor (CPU dies, GPU, NAND,
// battery). Sensors that share a product name keep the last reading.
func sensorTemps(ctx context.Context) ([]sensors.TemperatureStat, error) {
	if err := hidOpen(); err != nil {
		return nil, err
	}
	h := &hidTemps
	page, usage := int32(0xff00), int32(5)
	keys := [2]uintptr{cfCreateString("PrimaryUsagePage"), cfCreateString("PrimaryUsage")}
	vals := [2]uintptr{h.numberCreate(0, cfNumberSInt32, unsafe.Pointer(&page)), h.numberCreate(0, cfNumberSInt32, unsafe.Pointer(&usage))}
	var match uintptr
	if keys[0] != 0 && keys[1] != 0 && vals[0] != 0 && vals[1] != 0 {
		match = h.dictCreate(0, unsafe.Pointer(&keys[0]), unsafe.Pointer(&vals[0]), 2, h.keyCB, h.valCB)
	}
	runtime.KeepAlive(&page)
	runtime.KeepAlive(&usage)
	for i := range keys {
		cfRelease(keys[i])
		cfRelease(vals[i])
	}
	if match == 0 {
		return nil, errors.New("IOHID matching dictionary")
	}
	defer cfRelease(match)

	client := h.create(0)
	if client == 0 {
		return nil, errors.New("IOHIDEventSystemClientCreate")
	}
	defer cfRelease(client)
	h.setMatching(client, match)
	services := h.copyServices(client)
	if services == 0 {
		return nil, nil
	}
	defer cfRelease(services)
	product := cfCreateString("Product")
	if product == 0 {
		return nil, errors.New("CFStringCreateWithCString")
	}
	defer cfRelease(product)

	byName := map[string]float64{}
	n := h.arrayCount(services)
	for i := int64(0); i < n && ctx.Err() == nil; i++ {
		sc := h.arrayAt(services, i)
		if sc == 0 {
			continue
		}
		p := h.copyProperty(sc, product)
		name := cfText(p)
		cfRelease(p)
		if name == "" {
			continue
		}
		ev := h.copyEvent(sc, hidEventTemperature, 0, 0)
		if ev == 0 {
			continue
		}
		byName[name] = h.floatValue(ev, hidEventTemperature<<16)
		cfRelease(ev)
	}
	out := make([]sensors.TemperatureStat, 0, len(byName))
	for name, c := range byName {
		out = append(out, sensors.TemperatureStat{SensorKey: name, Temperature: c})
	}
	return out, ctx.Err()
}
