//go:build windows

package main

import "runtime"

// eoPeakRSSKB approximates the peak memory on Windows with the Go runtime's
// view (no getrusage): memory obtained from the OS.
func eoPeakRSSKB() int64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.Sys / 1024)
}
