//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// Hardware and OS facts from /proc and /etc (Linux; macOS has neither, so the
// values stay empty there). The Windows versions live in proc_windows.go.

func memTotalMB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var kb int
		if n, _ := fmt.Sscanf(sc.Text(), "MemTotal: %d kB", &kb); n == 1 {
			return kb / 1024
		}
	}
	return 0
}

func cpuModel() string {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "model name") {
			if i := strings.IndexByte(l, ':'); i > 0 {
				return strings.TrimSpace(l[i+1:])
			}
		}
	}
	return ""
}

func osName() string {
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "PRETTY_NAME=") {
				return strings.Trim(strings.TrimPrefix(l, "PRETTY_NAME="), `"`)
			}
		}
	}
	return runtime.GOOS
}
