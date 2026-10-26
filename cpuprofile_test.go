package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCPUProfileEnv(t *testing.T) {
	dir := t.TempDir()
	cpu, tr := filepath.Join(dir, "cpu.pprof"), filepath.Join(dir, "x.trace")
	t.Setenv("JAZZ_CPUPROFILE", cpu)
	t.Setenv("JAZZ_TRACE", tr)
	pfProfileStart()
	x := 0
	for i := 0; i < 1e6; i++ {
		x += i
	}
	_ = x
	pfProfileStop()
	pfProfileStop() // idempotent
	for _, p := range []string{cpu, tr} {
		if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
			t.Errorf("%s not written: %v", p, err)
		}
	}
}
