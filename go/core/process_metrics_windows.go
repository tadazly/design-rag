//go:build windows

package core

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

type processMemoryCounters struct {
	Size                       uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

var getProcessMemoryInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

func processSnapshot() (workingSetBytes uint64, cpuTimeMS int64) {
	handle, err := windows.GetCurrentProcess()
	if err != nil {
		return 0, 0
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err == nil {
		filetimeMilliseconds := func(value windows.Filetime) int64 {
			ticks := uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
			return int64(ticks / 10_000)
		}
		cpuTimeMS = filetimeMilliseconds(kernel) + filetimeMilliseconds(user)
	}
	counters := processMemoryCounters{Size: uint32(unsafe.Sizeof(processMemoryCounters{}))}
	result, _, _ := getProcessMemoryInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size))
	if result != 0 {
		workingSetBytes = uint64(counters.WorkingSetSize)
	}
	return workingSetBytes, cpuTimeMS
}
