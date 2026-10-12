//go:build windows

package agent

import (
	"context"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/yusufpapurcu/wmi"
	"golang.org/x/sys/windows"

	"vartio/internal/entities"
)

func platformGPUs(ctx context.Context) []entities.GPUInfo {
	nv := nvidiaGPUs(ctx)
	pdh := pdhGPUs()
	return assignGPUNames(dropIdleAdapters(mergeWindowsGPUs(nv, pdh)), videoControllerNames())
}

// GPU engine and adapter-memory counters cover NVIDIA, AMD and Intel.
// The query stays open so a later sample does not pay to rebuild it.
var pdhGPU struct {
	sync.Mutex
	failed bool
	query  uintptr
	engine uintptr
	used   uintptr
	limit  uintptr
}

const (
	pdhFmtDouble = 0x00000200
	pdhMoreData  = 0x800007D2
)

func pdhGPUs() []entities.GPUInfo {
	pdhGPU.Lock()
	defer pdhGPU.Unlock()
	if pdhGPU.failed {
		return nil
	}
	if pdhGPU.query == 0 && !pdhOpenGPU() {
		pdhGPU.failed = true
		return nil
	}
	if r, _, _ := procPdhCollect.Call(pdhGPU.query); r != 0 {
		return nil
	}
	engines := pdhArray(pdhGPU.engine)
	if len(engines) == 0 {
		return nil
	}
	return gpusFromCounters(engines, pdhArray(pdhGPU.used), pdhArray(pdhGPU.limit))
}

var (
	modpdh         = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpen    = modpdh.NewProc("PdhOpenQueryW")
	procPdhAdd     = modpdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollect = modpdh.NewProc("PdhCollectQueryData")
	procPdhArray   = modpdh.NewProc("PdhGetFormattedCounterArrayW")
	procPdhClose   = modpdh.NewProc("PdhCloseQuery")
)

func pdhOpenGPU() bool {
	var q uintptr
	if r, _, _ := procPdhOpen.Call(0, 0, uintptr(unsafe.Pointer(&q))); r != 0 {
		return false
	}
	if unsafe.Sizeof(pdhFmtItem{}) != 24 {
		procPdhClose.Call(q)
		return false
	}
	engine, okE := pdhAdd(q, `\GPU Engine(*)\Utilization Percentage`)
	used, okU := pdhAdd(q, `\GPU Adapter Memory(*)\Dedicated Usage`)
	limit, _ := pdhAdd(q, `\GPU Adapter Memory(*)\Dedicated Limit`)
	if !okE && !okU {
		procPdhClose.Call(q)
		return false
	}
	// One collect here so the sample below is not the counter's first, empty read.
	procPdhCollect.Call(q)
	pdhGPU.query = q
	pdhGPU.engine = engine
	pdhGPU.used = used
	pdhGPU.limit = limit
	return true
}

func pdhAdd(query uintptr, path string) (uintptr, bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	var h uintptr
	r, _, _ := procPdhAdd.Call(query, uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&h)))
	return h, r == 0
}

type pdhFmtItem struct {
	Name   *uint16
	Status uint32
	_      uint32
	Double float64
}

func pdhArray(counter uintptr) []gpuCounter {
	if counter == 0 {
		return nil
	}
	var size, count uint32
	r, _, _ := procPdhArray.Call(counter, pdhFmtDouble, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	// PDH_STATUS is a 32-bit LONG and comes back sign-extended.
	status := uint32(r)
	if (status != pdhMoreData && status != 0) || size == 0 {
		return nil
	}
	buf := make([]byte, size)
	r, _, _ = procPdhArray.Call(counter, pdhFmtDouble, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buf[0])))
	if uint32(r) != 0 || count == 0 {
		return nil
	}
	step := unsafe.Sizeof(pdhFmtItem{})
	if uintptr(len(buf)) < uintptr(count)*step {
		return nil
	}
	out := make([]gpuCounter, 0, count)
	for i := uint32(0); i < count; i++ {
		item := (*pdhFmtItem)(unsafe.Pointer(&buf[uintptr(i)*step]))
		// 0 is valid data, 1 is new data. Anything else is not a reading yet.
		if item.Status != 0 && item.Status != 1 || item.Name == nil {
			continue
		}
		out = append(out, gpuCounter{Instance: windows.UTF16PtrToString(item.Name), Value: item.Double})
	}
	return out
}

var videoNames struct {
	sync.Mutex
	at    time.Time
	names []string
}

func videoControllerNames() []string {
	videoNames.Lock()
	defer videoNames.Unlock()
	if time.Since(videoNames.at) < 10*time.Minute && videoNames.names != nil {
		return videoNames.names
	}
	videoNames.at = time.Now()
	names := queryVideoControllers()
	if names == nil {
		names = []string{}
	}
	videoNames.names = names
	return videoNames.names
}

func queryVideoControllers() []string {
	type controller struct{ Name string }
	done := make(chan []controller, 1)
	go func() {
		var dst []controller
		if err := wmi.Query("SELECT Name FROM Win32_VideoController", &dst); err != nil {
			done <- nil
			return
		}
		done <- dst
	}()
	var dst []controller
	select {
	case dst = <-done:
	case <-time.After(2 * time.Second):
		return nil
	}
	out := make([]string, 0, len(dst))
	for _, c := range dst {
		if name := strings.TrimSpace(c.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}
