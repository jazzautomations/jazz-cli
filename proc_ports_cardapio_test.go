//go:build !windows

package main

import (
	"net"
	"os"
	"testing"
)

// TestProcPortsCardapio runs the proc_ports offline script through the same
// path jazz uses (Toolbox.Exec -> script) and checks that a port we open shows
// up in the /proc listing and that its owner process resolves.
func TestProcPortsCardapio(t *testing.T) {
	if _, err := os.Stat("/proc/net/tcp"); err != nil {
		t.Skip("no /proc on this system")
	}
	tb, _ := scTestBox(t) // skips if python3 is missing

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback listener:", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	v, _ := scCall(t, tb, "proc_ports", map[string]any{"port": port, "limit": 2000})
	if !scOK(v) {
		t.Fatalf("proc_ports not ok: %v", v)
	}
	d := scDataOf(v)

	listening, _ := d["listening"].([]any)
	found := false
	for _, e := range listening {
		m, _ := e.(map[string]any)
		if p, _ := m["port"].(float64); int(p) == port {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("port %d not reported in listening: %v", port, d["listening"])
	}

	owner, ok := d["owner"].(map[string]any)
	if !ok {
		t.Fatalf("owner missing/null for a listening port: %v", d["owner"])
	}
	if p, _ := owner["port"].(float64); int(p) != port {
		t.Errorf("owner.port = %v, want %d", owner["port"], port)
	}
	// We opened the socket in this test process, same uid as the script, so the
	// owning pid must resolve (non-zero) and carry a command name.
	if pid, _ := owner["pid"].(float64); pid <= 0 {
		t.Errorf("owner.pid did not resolve: %v", owner)
	}
	if comm, _ := owner["comm"].(string); comm == "" {
		t.Errorf("owner.comm empty: %v", owner)
	}

	// Without a port: the process table must be non-empty and well-formed.
	v2, _ := scCall(t, tb, "proc_ports", map[string]any{"limit": 2000})
	if !scOK(v2) {
		t.Fatalf("proc_ports (process list) not ok: %v", v2)
	}
	procs, _ := scDataOf(v2)["processes"].([]any)
	if len(procs) == 0 {
		t.Fatalf("no processes returned")
	}
	first, _ := procs[0].(map[string]any)
	if pid, _ := first["pid"].(float64); pid <= 0 {
		t.Errorf("first process has no pid: %v", first)
	}
	if _, ok := scDataOf(v2)["owner"]; ok {
		t.Errorf("owner must be absent when no port is given: %v", scDataOf(v2)["owner"])
	}
}
