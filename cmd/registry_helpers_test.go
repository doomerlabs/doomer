package cmd

import "testing"

func TestLocalRegistryLoopbackHosts(t *testing.T) {
	for _, host := range []string{"localhost", "LOCALHOST:5000", "127.0.0.1", "127.0.0.1:5000", "127.0.0.2:5000", "::1", "[::1]", "[::1]:5000", "[0:0:0:0:0:0:0:1]:5000", "[::ffff:127.0.0.1]:5000"} {
		if !hasLocalhostPort(host) {
			t.Errorf("loopback registry %q was not recognized", host)
		}
	}
	for _, host := range []string{"registry.doomer.ai", "registry.doomer.ai:5000", "localhost.example:5000", "127.0.0.1.example:5000", "192.168.1.2:5000", "[::2]:5000", "[2001:db8::1]:5000"} {
		if hasLocalhostPort(host) {
			t.Errorf("remote registry %q was treated as loopback", host)
		}
	}
}
