package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// internetCheckURLs returns configured check URLs (comma-separated) with fallback.
func internetCheckURLs() []string {
	if Config != nil && strings.TrimSpace(Config.Network.InternetCheckURLs) != "" {
		var out []string
		for _, u := range strings.Split(Config.Network.InternetCheckURLs, ",") {
			u = strings.TrimSpace(u)
			if u != "" {
				out = append(out, u)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{
		"https://clients1.google.com/generate_204",
		"http://connectivitycheck.gstatic.com/generate_204",
	}
}

// checkInternet checks if internet is available, optionally through the active proxy
func checkInternet() bool {
	// If tunnel is active, we should check connectivity THROUGH the proxy
	// to ensure the tunnel itself is providing internet.
	if connState.IsActive() {
		return checkInternetViaProxy()
	}

	testURLs := internetCheckURLs()

	client := http.Client{Timeout: 5 * time.Second}
	for _, url := range testURLs {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				return true
			}
		}
	}
	return false
}

// checkInternetViaProxy attempts to reach a target using the local SOCKS5 proxy
func checkInternetViaProxy() bool {
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", Config.Network.ProxyPort)
	dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	if err != nil {
		return false
	}

	// Create a transport that uses the SOCKS5 proxy
	httpTransport := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.Dial(network, addr)
	}}

	client := http.Client{
		Transport: httpTransport,
		Timeout:   5 * time.Second,
	}

	for _, u := range internetCheckURLs() {
		resp, err := client.Get(u)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				return true
			}
		}
	}

	return false
}

// checkSocks5 performs a real SOCKS5 handshake (not just TCP dial).
func checkSocks5(port int) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	// SOCKS5 greeting: VER=5, NMETHODS=1, METHODS=0 (no auth)
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return false
	}
	reply := make([]byte, 2)
	n, err := conn.Read(reply)
	if err != nil || n != 2 {
		return false
	}
	return reply[0] == 0x05 && reply[1] == 0x00
}

// checkProxyConnectivity verifies SOCKS5 on the production port via handshake.
func checkProxyConnectivity() bool {
	return checkSocks5(Config.Network.ProxyPort)
}

// checkProxyConnectivityOnPort verifies SOCKS5 on the given port via handshake.
// Used for pre-flight testing of a tunnel on a temporary port without affecting
// the production tunnel.
func checkProxyConnectivityOnPort(port int) bool {
	return checkSocks5(port)
}

// tcpPortOpen is the fast pre-check for combo SSH probing (TCP 22 before ssh).
func tcpPortOpen(host string, port string, timeout time.Duration) bool {
	if host == "" {
		return false
	}
	if port == "" {
		port = "22"
	}
	addr := net.JoinHostPort(host, port)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
