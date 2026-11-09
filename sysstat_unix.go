//go:build !windows

package main

import (
	"os"
	"syscall"
)

// sysStatUG extrai uid/gid do FileInfo — unix tem Stat_t, windows não.
func sysStatUG(fi os.FileInfo) (uid, gid uint32, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return st.Uid, st.Gid, true
}
