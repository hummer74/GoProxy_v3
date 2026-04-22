package main

import (
        "context"
        "fmt"
        "net"
        "net/http"
        "time"

        "golang.org/x/net/proxy"
)

// checkInternet checks if internet is available, optionally through the active proxy
func checkInternet() bool {
        // If tunnel is active, we should check connectivity THROUGH the proxy
        // to ensure the tunnel itself is providing internet.
        if connState.IsActive() {
                return checkInternetViaProxy()
        }

        // Normal check for direct internet
        testURLs := []string{
                "https://clients1.google.com/generate_204",
                "http://connectivitycheck.gstatic.com/generate_204",
        }

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

        // Check Google via Proxy
        resp, err := client.Get("https://clients1.google.com/generate_204")
        if err == nil {
                resp.Body.Close()
                return resp.StatusCode == 204
        }

        return false
}

// checkProxyConnectivity verifies if the SOCKS5 port is actually open and responding
func checkProxyConnectivity() bool {
        addr := fmt.Sprintf("127.0.0.1:%d", Config.Network.ProxyPort)
        conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
        if err != nil {
                return false
        }
        conn.Close()
        return true
}

// checkProxyConnectivityOnPort verifies if a SOCKS5 proxy is listening on the given port.
// Used for pre-flight testing of a tunnel on a temporary port without affecting
// the production tunnel.
func checkProxyConnectivityOnPort(port int) bool {
        addr := fmt.Sprintf("127.0.0.1:%d", port)
        conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
        if err != nil {
                return false
        }
        conn.Close()
        return true
}