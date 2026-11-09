//go:build windows

package main

import "os"

// sysStatUG — windows não tem uid/gid estilo POSIX; o chamador
// responde "stat_t indisponível" e o selo fica unverified (honesto).
func sysStatUG(fi os.FileInfo) (uid, gid uint32, ok bool) {
	return 0, 0, false
}
