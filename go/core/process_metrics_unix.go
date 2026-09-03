//go:build !windows

package core

import (
	"runtime"
	"syscall"
)

func timevalMilliseconds(value syscall.Timeval) int64 {
	return value.Sec*1_000 + int64(value.Usec)/1_000
}

func processSnapshot() (workingSetBytes uint64, cpuTimeMS int64) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, 0
	}
	workingSetBytes = uint64(usage.Maxrss)
	if runtime.GOOS != "darwin" {
		workingSetBytes *= 1024
	}
	cpuTimeMS = timevalMilliseconds(usage.Utime) + timevalMilliseconds(usage.Stime)
	return workingSetBytes, cpuTimeMS
}
