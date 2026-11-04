package main

import (
	"net"
	"os"
	"testing"
)

// Live tests against the real /proc. Each skips when procfs is not mounted
// (non-Linux, or a container without it) so cross-builds still compile.

func pfSkipNoProc(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/proc/self"); err != nil {
		t.Skip("no /proc")
	}
}

func TestProcfsMemInfo(t *testing.T) {
	pfSkipNoProc(t)
	m, err := pfMemInfo()
	if err != nil {
		t.Fatal(err)
	}
	if m["MemTotal"] == 0 {
		t.Error("MemTotal missing or 0")
	}
	if m["MemAvailable"] == 0 {
		t.Error("MemAvailable missing or 0")
	}
}

func TestProcfsProcessesSelf(t *testing.T) {
	pfSkipNoProc(t)
	procs, err := pfProcesses()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range procs {
		if p.PID == os.Getpid() {
			if p.Comm == "" {
				t.Error("own Comm is empty")
			}
			if p.PPID <= 0 {
				t.Errorf("own PPID = %d", p.PPID)
			}
			return
		}
	}
	t.Fatalf("own pid %d absent from pfProcesses", os.Getpid())
}

func TestProcfsPIDAlive(t *testing.T) {
	pfSkipNoProc(t)
	if !pfPIDAlive(os.Getpid()) {
		t.Error("own pid not alive")
	}
	if pfPIDAlive(1 << 30) {
		t.Error("absurdly high pid reported alive")
	}
	if pfPIDAlive(0) || pfPIDAlive(-7) {
		t.Error("non-positive pid reported alive")
	}
}

func TestProcfsListeningPorts(t *testing.T) {
	pfSkipNoProc(t)
	ports, err := pfListeningPorts()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ports {
		if p.Port <= 0 || p.Port > 65535 {
			t.Errorf("bad port %d", p.Port)
		}
		if p.Inode == 0 {
			t.Errorf("zero inode on port %d", p.Port)
		}
		if p.Proto != "tcp4" && p.Proto != "tcp6" {
			t.Errorf("bad proto %q", p.Proto)
		}
	}
	t.Logf("%d listening sockets", len(ports))
}

// pfPortOwner against a listener we own must come back with our own pid.
func TestProcfsPortOwnerSelf(t *testing.T) {
	pfSkipNoProc(t)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	pid, ok := pfPortOwner(port)
	if !ok {
		t.Fatalf("pfPortOwner(%d): no owner found", port)
	}
	if pid != os.Getpid() {
		t.Fatalf("pfPortOwner(%d) = pid %d, want own pid %d", port, pid, os.Getpid())
	}
}
