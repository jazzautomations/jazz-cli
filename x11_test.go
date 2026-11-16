//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestX11Socket(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{":0", "/tmp/.X11-unix/X0"},
		{":0.0", "/tmp/.X11-unix/X0"},
		{":2", "/tmp/.X11-unix/X2"},
		{"localhost:10.0", ""}, // SSH forward is TCP, not a unix socket
		{"host:0", ""},
		{":abc", ""},
		{":", ""},
		{"", ""},
	} {
		if got := x11Socket(tc.in); got != tc.want {
			t.Errorf("x11Socket(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestX11AutodetectRespectsExisting(t *testing.T) {
	t.Setenv("DISPLAY", ":9")
	t.Setenv("JAZZ_X11", "")
	x11Autodetect()
	if os.Getenv("DISPLAY") != ":9" {
		t.Fatalf("DISPLAY overwritten: %q", os.Getenv("DISPLAY"))
	}
}

func TestX11AutodetectOff(t *testing.T) {
	os.Unsetenv("DISPLAY")
	os.Unsetenv("WAYLAND_DISPLAY")
	t.Setenv("JAZZ_X11", "off")
	x11Autodetect()
	if os.Getenv("DISPLAY") != "" {
		t.Fatalf("JAZZ_X11=off ignored: %q", os.Getenv("DISPLAY"))
	}
}

func TestX11XauthPrefersHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	xa := filepath.Join(home, ".Xauthority")
	if err := os.WriteFile(xa, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := x11Xauth(); got != xa {
		t.Fatalf("x11Xauth = %q, want %q", got, xa)
	}
}

// The X11 binds re-expose the socket the /tmp tmpfs hid. They are resolved at
// sbSetup (fields), not inside sbBwrapArgs, so the golden-argv tests stay pure.
func TestBwrapArgsReexposesX11(t *testing.T) {
	sb := &Sandbox{Mode: "strict", Backend: sbBwrap, Bin: "/usr/bin/bwrap",
		Tmp: "/tmp/x", GUISock: "/tmp/.X11-unix/X0", GUIAuth: "/u/.Xauthority"}
	a := strings.Join(sbBwrapArgs(sb, ""), " ")
	if !strings.Contains(a, "--ro-bind-try /tmp/.X11-unix/X0 /tmp/.X11-unix/X0") {
		t.Fatalf("socket bind ausente: %s", a)
	}
	if !strings.Contains(a, "--ro-bind-try /u/.Xauthority /u/.Xauthority") {
		t.Fatalf("xauth bind ausente: %s", a)
	}
	// sem display resolvido, nada muda
	sb2 := &Sandbox{Mode: "strict", Backend: sbBwrap, Bin: "/usr/bin/bwrap", Tmp: "/tmp/x"}
	for _, tok := range sbBwrapArgs(sb2, "") {
		if strings.Contains(tok, "X11-unix") || strings.Contains(tok, "Xauthority") {
			t.Fatalf("bind X11 sem display: %v", sbBwrapArgs(sb2, ""))
		}
	}
}
