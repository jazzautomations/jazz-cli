//go:build windows

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// pfTryLock takes a non-blocking exclusive LockFileEx on the first byte of f
// (the Windows counterpart of flock LOCK_EX|LOCK_NB). busy means another
// process holds it: poll again.
func pfTryLock(f *os.File) (busy bool, err error) {
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return true, nil
	}
	return false, err
}

func pfUnlock(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
