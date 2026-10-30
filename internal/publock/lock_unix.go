//go:build !windows

package publock

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func lockFile(f *os.File, shared bool) error {
	mode := unix.LOCK_EX
	if shared {
		mode = unix.LOCK_SH
	}
	return unix.Flock(int(f.Fd()), mode)
}
func unlockFile(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }

func tryLockFile(f *os.File, shared bool) error {
	mode := unix.LOCK_EX
	if shared {
		mode = unix.LOCK_SH
	}
	return unix.Flock(int(f.Fd()), mode|unix.LOCK_NB)
}

// Busy reports an OS lock held by another process.
func Busy(err error) bool { return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) }
