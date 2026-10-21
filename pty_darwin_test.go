package main

import (
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ptyOpen opens a pseudo-terminal pair (no dependency: posix_openpt by hand).
func ptyOpen() (master *os.File, slave string, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		unix.Close(fd)
		return nil, "", err
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		unix.Close(fd)
		return nil, "", err
	}
	var buf [128]byte
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
		unix.Close(fd)
		return nil, "", e
	}
	name := string(buf[:])
	if i := strings.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	return os.NewFile(uintptr(fd), "/dev/ptmx"), name, nil
}
