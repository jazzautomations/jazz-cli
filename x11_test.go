//go:build linux

package main

import (
	"os"
	"path/filepath"
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
