//go:build !linux && !darwin

package livewatch

import (
	"fmt"
	"net"
)

func endpoint(string) (string, error) {
	return "", fmt.Errorf("watch control sockets currently support Linux and macOS")
}
func listen(string) (net.Listener, error) {
	return nil, fmt.Errorf("watch control sockets currently support Linux and macOS")
}
func ownPeer(net.Conn) bool { return false }
