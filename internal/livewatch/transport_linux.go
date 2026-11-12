//go:build linux

package livewatch

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// Linux abstract sockets live entirely in the kernel: no pathname, PID file,
// lock file, or cleanup marker is created on the filesystem.
func endpoint(key string) (string, error) {
	return fmt.Sprintf("@doomer-watch-%d-%s", os.Getuid(), key), nil
}
func listen(key string) (net.Listener, error) {
	address, _ := endpoint(key)
	return net.Listen("unix", address)
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
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		allowed = e == nil && cred.Uid == uint32(os.Getuid())
	}) != nil {
		return false
	}
	return allowed
}
