package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// spSbinDirs are appended to PATH on Linux when they exist and are missing. Debian-family
// distros (antiX, Devuan, Debian) leave /sbin and /usr/sbin out of a normal user's PATH, so
// `iw`, `iwlist`, `modprobe`, `ip`… looked "not found" to the model and it gave up or told the
// user to install things that were already there (d610 wifi case, 09/11). Appending, never
// prepending, keeps every binary the user already resolves exactly as before.
var spSbinDirs = []string{"/usr/local/sbin", "/usr/sbin", "/sbin"}

// spWithSbin returns path with the existing sbin dirs appended (unchanged when none is missing).
func spWithSbin(path string, exists func(string) bool) string {
	have := map[string]bool{}
	for _, d := range filepath.SplitList(path) {
		have[filepath.Clean(d)] = true
	}
	var add []string
	for _, d := range spSbinDirs {
		if !have[d] && exists(d) {
			add = append(add, d)
		}
	}
	if len(add) == 0 {
		return path
	}
	if path == "" {
		return strings.Join(add, string(os.PathListSeparator))
	}
	return path + string(os.PathListSeparator) + strings.Join(add, string(os.PathListSeparator))
}

// spFixPath applies spWithSbin to the process environment (Linux only), so the bash tool,
// the sandbox and the offline brain all inherit it.
func spFixPath() {
	if runtime.GOOS != "linux" {
		return
	}
	isDir := func(d string) bool { st, err := os.Stat(d); return err == nil && st.IsDir() }
	p := os.Getenv("PATH")
	if np := spWithSbin(p, isDir); np != p {
		_ = os.Setenv("PATH", np)
	}
}
