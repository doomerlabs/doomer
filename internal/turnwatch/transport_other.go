//go:build !linux && !darwin

package turnwatch

import (
	"fmt"
	"net"
)

func endpoint(string) (string, error) {
	return "", fmt.Errorf("turn workers currently support Linux and macOS")
}
func listen(string) (net.Listener, error) {
	return nil, fmt.Errorf("turn workers currently support Linux and macOS")
}
func ownPeer(net.Conn) bool { return false }
