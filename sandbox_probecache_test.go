package main

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSbProbeCache(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no probe to cache on " + runtime.GOOS)
	}
	pfTestHome(t)
	b0, bin0, why0 := sbProbeOS()
	b1, bin1, why1 := sbProbeCached()
	if b0 != b1 || bin0 != bin1 || why0 != why1 {
		t.Fatalf("cached probe differs: %q %q %q vs %q %q %q", b0, bin0, why0, b1, bin1, why1)
	}
	_, err := os.Stat(sbProbeCachePath())
	if b0 == "" {
		if err == nil {
			t.Fatal("a negative verdict was cached")
		}
		t.Skip("no sandbox backend here: " + why0)
	}
	if err != nil {
		t.Fatal("positive verdict not cached")
	}
	key := sbProbeKey(bin0)
	if b, bn, ok := sbProbeCacheRead(key); !ok || b != b0 || bn != bin0 {
		t.Fatalf("cache read = %q %q %v", b, bn, ok)
	}
	t0 := time.Now()
	if b, _, _ := sbProbeCached(); b != b0 {
		t.Fatal("cached verdict changed")
	}
	t.Logf("probe from cache: %s", time.Since(t0))
	// another key (binary/kernel/userns changed) misses
	if _, _, ok := sbProbeCacheRead(strings.Repeat("0", 64)); ok {
		t.Error("a different key hit the cache")
	}
	// stale entries miss
	raw, _ := os.ReadFile(sbProbeCachePath())
	f := strings.Split(strings.TrimSpace(string(raw)), "\t")
	f[4] = "1000"
	os.WriteFile(sbProbeCachePath(), []byte(strings.Join(f, "\t")), 0o600)
	if _, _, ok := sbProbeCacheRead(key); ok {
		t.Error("a week-old entry hit the cache")
	}
	// a forged backend name is refused
	f[4], f[2] = "9999999999", "none"
	os.WriteFile(sbProbeCachePath(), []byte(strings.Join(f, "\t")), 0o600)
	if _, _, ok := sbProbeCacheRead(key); ok {
		t.Error("future/forged entry hit the cache")
	}
	sbProbeForget()
	if _, err := os.Stat(sbProbeCachePath()); err == nil {
		t.Error("forget left the file")
	}
}

func BenchmarkSbProbe(b *testing.B) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		b.Skip()
	}
	home := b.TempDir()
	b.Setenv("JAZZ_HOME", home)
	b.Run("os", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sbProbeOS()
		}
	})
	b.Run("cached", func(b *testing.B) {
		sbProbeCached()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sbProbeCached()
		}
	})
}
