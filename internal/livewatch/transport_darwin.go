//go:build darwin

package livewatch

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// macOS requires a socket pathname shorter than 104 bytes. Use /tmp directly:
// os.TempDir() can exceed that limit on macOS and can differ between the watcher
// and its clients. Keep the UID and key in the name for isolation; permissions
// and peer credentials restrict access. UnixListener removes it on Close.
func endpoint(key string) (string, error) {
	return fmt.Sprintf("/tmp/doomer-%d-%s.sock", os.Getuid(), key[:32]), nil
}
func listen(key string) (net.Listener, error) {
	address, _ := endpoint(key)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		return nil, err
	}
	l.SetUnlinkOnClose(true)
	if err := os.Chmod(address, 0600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}
func ownPeer(conn net.Conn) bool {
	u, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := u.SyscallConn()
	if err != nil {
		return false
	}
	allowed := false
	if raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		allowed = e == nil && cred.Uid == uint32(os.Getuid())
	}) != nil {
		return false
	}
	return allowed
}
