//go:build !windows

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// pfTryLock takes a non-blocking exclusive flock on f. busy means another
// process holds it (or the call was interrupted): poll again.
func pfTryLock(f *os.File) (busy bool, err error) {
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
		return true, nil
	}
	return false, err
}

func pfUnlock(f *os.File) { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
