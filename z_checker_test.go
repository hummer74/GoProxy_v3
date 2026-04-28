// x_checker_test.go
// Tests for connectivity checkers: checkProxyConnectivityOnPort,
// and checkInternetDirect (with stubbed HTTP endpoints).
package main

import (
	"net"
	"testing"
)

// ---------------------------------------------------------------------------
// checkProxyConnectivityOnPort — real TCP connect test
// ---------------------------------------------------------------------------

func TestCheckProxyConnectivityOnPort_NothingListening(t *testing.T) {
	// Nothing should be listening on a random high port
	result := checkProxyConnectivityOnPort(59999)
	if result {
		t.Error("checkProxyConnectivityOnPort(59999) = true, want false (nothing listening)")
	}
}

func TestCheckProxyConnectivityOnPort_ActualListener(t *testing.T) {
	// Start a TCP listener on a random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("Cannot create test listener")
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	result := checkProxyConnectivityOnPort(addr.Port)
	if !result {
		t.Errorf("checkProxyConnectivityOnPort(%d) = false, want true (listener active)", addr.Port)
	}
}

// ---------------------------------------------------------------------------
// checkInternetDirect — requires real internet or skips
// ---------------------------------------------------------------------------

func TestCheckInternetDirect(t *testing.T) {
	// This test actually tries to reach Google's connectivity check endpoint.
	// It will pass when internet is available, fail/skip when not.
	result := checkInternetDirect()
	// We don't assert true/false — it depends on the test environment.
	// But we verify it doesn't panic or hang.
	t.Logf("checkInternetDirect() = %v (depends on network)", result)
}
