//go:build darwin

package livewatch

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestEndpointIgnoresTMPDIR(t *testing.T) {
	key := Key(t.Name())
	want := fmt.Sprintf("/tmp/doomer-%d-%s.sock", os.Getuid(), key[:32])
	for _, tempDir := range []string{
		"/var/folders/wg/9171rpls5nv8m_1ql0fmy8tw0000gn/T/nix-shell.B9PzHc",
		"/tmp/" + strings.Repeat("long", 100),
		"/tmp/another-client",
	} {
		t.Setenv("TMPDIR", tempDir)
		address, err := endpoint(key)
		if err != nil {
			t.Fatal(err)
		}
		if address != want {
			t.Fatalf("endpoint = %q, want %q", address, want)
		}
		if len(address) >= len(unix.RawSockaddrUnix{}.Path) {
			t.Fatalf("endpoint exceeds macOS socket path limit: %q", address)
		}
	}
}

func TestDarwinSocketAcrossTMPDIRs(t *testing.T) {
	key := Key(t.TempDir())
	t.Setenv("TMPDIR", "/tmp/"+strings.Repeat("long", 100))
	listener, err := listen(key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	address, err := endpoint(key)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(address)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		t.Fatalf("socket mode = %v, want socket with 0600 permissions", info.Mode())
	}
	if other, err := listen(key); err == nil {
		other.Close()
		t.Fatal("second listener replaced the active socket")
	}

	// A client with a different TMPDIR must still find the original watcher.
	t.Setenv("TMPDIR", "/tmp/different-client")
	clientAddress, err := endpoint(key)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("unix", clientAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if !ownPeer(conn) {
		t.Fatal("client did not authenticate the listener's UID")
	}
	if err := listener.(*net.UnixListener).SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	peer, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if !ownPeer(peer) {
		t.Fatal("listener did not authenticate the client's UID")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(address); !os.IsNotExist(err) {
		t.Fatalf("socket still exists after close: %v", err)
	}
}
