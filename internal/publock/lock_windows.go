//go:build windows

package publock

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
)

func lockFile(f *os.File, shared bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if shared {
		flags = 0
	}
	var o windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &o)
}
func unlockFile(f *os.File) error {
	var o windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &o)
}

func tryLockFile(f *os.File, shared bool) error {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if !shared {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	var o windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &o)
}

// Busy reports an OS lock held by another process.
func Busy(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }
