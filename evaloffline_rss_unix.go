//go:build !windows

package main

import (
	"runtime"
	"syscall"
)

// eoPeakRSSKB is the peak resident set size of this process (KB).
func eoPeakRSSKB() int64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss) / 1024 // bytes on macOS
	}
	return int64(ru.Maxrss) // KB on Linux
}
