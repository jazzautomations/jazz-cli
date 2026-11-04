package main

import (
	"os"
	"strings"
	"testing"
)

// o sandbox compartilha o PID namespace do host só sob --yolo (ou env), pra
// tarefas de gerência de processo; o default mantém --unshare-pid.
func TestSandboxHostPIDArgs(t *testing.T) {
	sb := sbNew("auto", sbBwrap, "/usr/bin/bwrap", true, []string{t.TempDir()})
	def := strings.Join(sbBwrapArgs(sb, ""), " ")
	if !strings.Contains(def, "--unshare-pid") {
		t.Fatalf("default deve isolar o PID (--unshare-pid): %s", def)
	}
	sb.HostPID = true
	on := strings.Join(sbBwrapArgs(sb, ""), " ")
	if strings.Contains(on, "--unshare-pid") {
		t.Fatalf("HostPID=true não pode ter --unshare-pid: %s", on)
	}
	if !strings.Contains(on, "--new-session") {
		t.Fatalf("--new-session deve permanecer: %s", on)
	}
}

func TestSbHostPIDAllowed(t *testing.T) {
	os.Unsetenv("JAZZ_SANDBOX_HOST_PID")
	if sbHostPIDAllowed(&Config{ModeOverride: "auto"}) {
		t.Error("auto não deve compartilhar PID do host")
	}
	if !sbHostPIDAllowed(&Config{ModeOverride: "yolo"}) {
		t.Error("yolo deve compartilhar PID do host")
	}
	t.Setenv("JAZZ_SANDBOX_HOST_PID", "0")
	if sbHostPIDAllowed(&Config{ModeOverride: "yolo"}) {
		t.Error("env=0 deve vencer o yolo e isolar")
	}
	t.Setenv("JAZZ_SANDBOX_HOST_PID", "1")
	if !sbHostPIDAllowed(&Config{ModeOverride: "auto"}) {
		t.Error("env=1 deve forçar o compartilhamento")
	}
}
