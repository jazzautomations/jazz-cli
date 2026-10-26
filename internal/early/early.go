// Package early tunes the garbage collector before the package init of
// jazz's main package runs. Imported packages initialize before the
// importing one, so this init happens ahead of main's ~1.6k init-time
// allocations and long before main() (docs/research/low-level-performance.md
// §3.1, item 2 of the top 10).
//
// Two knobs:
//
//   - single-core machines (the reference Pentium M): GOGC 400 and a 384 MB
//     soft memory limit for the whole process, because there the collector's
//     marking time is wall time;
//   - short commands (`jazz version`, `jazz -p ...`): the GC is off from
//     start until the caller says the short part is over (Restore), with the
//     memory limit still in force and a safety timer that restores it anyway.
//
// A GOGC set by the user always wins: nothing here touches the collector then.
package early

import (
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"time"
)

// SoftLimit is the memory limit installed when GOMEMLIMIT is not set: on a
// 2 GB box the process must still collect before swapping.
const SoftLimit = 384 << 20

// SafetyRestore bounds how long the GC can stay off if a caller forgets to
// call Restore (or the short path turns into a long one).
const SafetyRestore = 3 * time.Second

var (
	userGOGC  = os.Getenv("GOGC") != ""
	userLimit = os.Getenv("GOMEMLIMIT") != ""

	mu       sync.Mutex
	off      bool  // the GC is currently disabled by Off
	percent  = 100 // what Restore puts back
	limit    = int64(math.MaxInt64)
	timer    *time.Timer
	tunedCPU bool
)

func init() {
	if userGOGC {
		return
	}
	if runtime.NumCPU() == 1 {
		tunedCPU = true
		percent = 400
		debug.SetGCPercent(percent)
		if !userLimit {
			limit = SoftLimit
			debug.SetMemoryLimit(limit)
		}
	}
	if ShortArgs(os.Args[1:]) {
		Off()
	}
}

// ShortArgs reports whether the command line is one of the short commands
// that run with the GC off: `version`/`--version` and headless `-p`.
func ShortArgs(args []string) bool {
	for i, a := range args {
		switch a {
		case "-p", "--p", "-print", "--print", "-p=true", "--print=true",
			"-version", "--version":
			return true
		case "--":
			return false
		}
		if i == 0 && a == "version" {
			return true
		}
	}
	return false
}

// Off disables the GC until Restore (keeping a soft memory limit so a
// runaway path still collects) and arms the safety timer. No-op when the
// user set GOGC.
func Off() {
	if userGOGC {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if off {
		return
	}
	off = true
	debug.SetGCPercent(-1)
	if !userLimit && limit == math.MaxInt64 {
		debug.SetMemoryLimit(SoftLimit)
	}
	timer = time.AfterFunc(SafetyRestore, Restore)
}

// Restore turns the GC back on with the tuned settings. Safe to call any
// number of times, from any goroutine.
func Restore() {
	mu.Lock()
	defer mu.Unlock()
	if !off {
		return
	}
	off = false
	if timer != nil {
		timer.Stop()
		timer = nil
	}
	debug.SetGCPercent(percent)
	if !userLimit {
		debug.SetMemoryLimit(limit)
	}
}

// IsOff reports whether the GC is currently disabled by this package.
func IsOff() bool {
	mu.Lock()
	defer mu.Unlock()
	return off
}

// Tuned reports whether the single-core settings were applied.
func Tuned() bool { return tunedCPU }
