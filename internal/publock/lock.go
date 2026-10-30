package publock

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

type Lock struct{ file *os.File }

func Acquire(root, key string) (*Lock, error) {
	return acquire(root, key, false, false)
}

// AcquireShared protects read-only consumers while allowing concurrent readers.
func AcquireShared(root, key string) (*Lock, error) {
	return acquire(root, key, true, false)
}

func TryAcquire(root, key string) (*Lock, error) { return acquire(root, key, false, true) }

func acquire(root, key string, shared, nonblocking bool) (*Lock, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer rooted.Close()
	if err := rooted.MkdirAll(".publication-locks", 0700); err != nil {
		return nil, err
	}
	path := filepath.ToSlash(filepath.Join(".publication-locks", fmt.Sprintf("%x.lock", sha256.Sum256([]byte(key)))))
	f, err := rooted.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if os.IsNotExist(err) {
		if e := rooted.MkdirAll(".publication-locks", 0700); e == nil {
			f, err = rooted.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		}
	}
	if err != nil {
		return nil, err
	}
	lock := lockFile
	if nonblocking {
		lock = tryLockFile
	}
	if err := lock(f, shared); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{file: f}, nil
}
func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
