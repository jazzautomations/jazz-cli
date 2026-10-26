package main

import (
	"fmt"
	"os"
	"runtime/pprof"
	"runtime/trace"
	"sync"
)

// JAZZ_CPUPROFILE=path writes a CPU profile of the whole process (pprof
// format; `go tool pprof jazz path`). It is the input of the PGO step of
// docs/research/low-level-performance.md §9: profiles are collected on the
// reference machine and merged into default.pgo. Every exit of main goes
// through pfQuit (and the crash path through pfExit) so the profile is flushed even on os.Exit paths.
//
// JAZZ_TRACE=path does the same with runtime/trace (scheduler, syscalls,
// GC, child processes waited on): `go tool trace path`.
var (
	pfProfMu    sync.Mutex
	pfProfFile  *os.File
	pfTraceFile *os.File
)

func pfProfileStart() {
	if p := os.Getenv("JAZZ_TRACE"); p != "" {
		if f, err := os.Create(p); err != nil {
			fmt.Fprintln(os.Stderr, "jazz: JAZZ_TRACE:", err)
		} else if err := trace.Start(f); err != nil {
			fmt.Fprintln(os.Stderr, "jazz: JAZZ_TRACE:", err)
			f.Close()
		} else {
			pfProfMu.Lock()
			pfTraceFile = f
			pfProfMu.Unlock()
		}
	}
	p := os.Getenv("JAZZ_CPUPROFILE")
	if p == "" {
		return
	}
	f, err := os.Create(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jazz: JAZZ_CPUPROFILE:", err)
		return
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		fmt.Fprintln(os.Stderr, "jazz: JAZZ_CPUPROFILE:", err)
		f.Close()
		return
	}
	pfProfMu.Lock()
	pfProfFile = f
	pfProfMu.Unlock()
}

// pfProfileStop flushes the profile; safe to call more than once.
func pfProfileStop() {
	pfProfMu.Lock()
	defer pfProfMu.Unlock()
	if pfTraceFile != nil {
		trace.Stop()
		pfTraceFile.Close()
		pfTraceFile = nil
	}
	if pfProfFile == nil {
		return
	}
	pprof.StopCPUProfile()
	pfProfFile.Close()
	pfProfFile = nil
}

// pfQuit is os.Exit for main: it flushes the CPU profile first.
func pfQuit(code int) {
	pfProfileStop()
	os.Exit(code)
}
