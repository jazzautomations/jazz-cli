package main

// x11.go: display discovery for GUI opens.
//
// A jazz run over SSH on a machine whose user is logged into X (the d610
// in the living room) has DISPLAY unset — then "abre na interface gráfica"
// either fails silently or gets narrated as success. When the same user
// owns an X socket, expose that display and its xauth so xdg-open is real;
// when none exists the env stays unset and the opener fails honestly.
// Disable with JAZZ_X11=off.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// x11Autodetect sets DISPLAY (and XAUTHORITY) in the process environment
// when the user owns a local X server and neither is set. It runs once at
// startup so shells, tools and the sandbox all inherit it.
func x11Autodetect() {
	if runtime.GOOS != "linux" || olOffWord(os.Getenv("JAZZ_X11")) {
		return
	}
	if os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" {
		return
	}
	// A usable display needs a socket and a readable cookie. The socket is
	// usually owned by root or the display manager (d610: Xorg-as-root with
	// a world-writable X0), never by the user — ownership is not the test;
	// the cookie is. With no xauth at all, leave DISPLAY unset so the
	// opener fails honestly instead of halfway.
	x := x11Xauth()
	if x == "" {
		return
	}
	for n := 0; n <= 7; n++ {
		fi, err := os.Stat(fmt.Sprintf("/tmp/.X11-unix/X%d", n))
		if err != nil || fi.Mode()&os.ModeSocket == 0 {
			continue
		}
		os.Setenv("DISPLAY", fmt.Sprintf(":%d", n))
		os.Setenv("XAUTHORITY", x)
		return
	}
}

// x11Xauth finds a readable xauthority for the just-discovered display:
// the usual per-user spots, then display-manager dirs (slimski, lightdm).
func x11Xauth() string {
	var c []string
	if h, err := os.UserHomeDir(); err == nil {
		c = append(c, filepath.Join(h, ".Xauthority"))
	}
	c = append(c,
		fmt.Sprintf("/run/user/%d/.Xauthority", os.Getuid()),
		fmt.Sprintf("/run/user/%d/gdm/Xauthority", os.Getuid()),
		"/var/run/slimski.auth",
		"/var/lib/slimski.auth",
	)
	if ds, err := filepath.Glob("/var/run/lightdm/*/xauthority"); err == nil {
		c = append(c, ds...)
	}
	for _, p := range c {
		if f, err := os.Open(p); err == nil { // open probes readability, stat only existence
			f.Close()
			return p
		}
	}
	return ""
}

// x11Socket maps a DISPLAY value to its unix socket (":0", ":0.0" →
// /tmp/.X11-unix/X0). "" for host:display TCP forwards — no socket to bind.
func x11Socket(display string) string {
	if !strings.HasPrefix(display, ":") {
		return ""
	}
	n := strings.TrimPrefix(display, ":")
	if i := strings.IndexByte(n, '.'); i >= 0 {
		n = n[:i]
	}
	for _, r := range n {
		if r < '0' || r > '9' {
			return ""
		}
	}
	if n == "" {
		return ""
	}
	return "/tmp/.X11-unix/X" + n
}
