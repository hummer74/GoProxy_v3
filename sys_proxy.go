package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	windows "golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// CurrentPACContent holds the latest generated PAC logic for reference
var CurrentPACContent string

// pacServer holds the running PAC HTTP server reference for graceful shutdown.
var (
	pacServer   *http.Server
	pacServerMu sync.Mutex
)

// setSystemProxy configures Windows Internet Settings for PAC URL
func setSystemProxy(pacURL string) bool {
	debugLog("PROXY", "Setting system proxy PAC: %s", pacURL)
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, windows.KEY_SET_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()

	if err := k.SetStringValue("AutoConfigURL", pacURL); err != nil {
		return false
	}

	if err := k.SetDWordValue("ProxyEnable", 0); err != nil {
		return false
	}

	return true
}

// disableSystemProxy clears OUR PAC and disables the proxy in Windows Internet
// Settings. A foreign AutoConfigURL (corporate proxy, another tool) is left
// untouched — blanking it would break the user's network.
func disableSystemProxy() {
	debugLog("PROXY", "Disabling system proxy")
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, windows.KEY_QUERY_VALUE|windows.KEY_SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()

	if cur, _, err := k.GetStringValue("AutoConfigURL"); err == nil && cur != "" && !isOurPAC(cur) {
		logTunnelEvent("WARN", "proxy", fmt.Sprintf("Leaving foreign AutoConfigURL in place: %s", cur))
		return
	}
	_ = k.SetStringValue("AutoConfigURL", "")
	_ = k.SetDWordValue("ProxyEnable", 0)
}

// isOurPAC reports whether a PAC URL is ours (x_proxy.pac served on our port).
func isOurPAC(url string) bool {
	if !strings.Contains(url, "x_proxy.pac") {
		return false
	}
	return strings.Contains(url, strconv.Itoa(Config.Network.PACHttpPort))
}

// stopPACServer gracefully shuts down the PAC HTTP server with a 3-second timeout.
// Falls back to force-close if graceful shutdown times out.
func stopPACServer() {
	debugLog("PAC", "Stopping PAC server")
	pacServerMu.Lock()
	server := pacServer
	pacServer = nil
	pacServerMu.Unlock()

	if server == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		// Graceful shutdown failed — force close
		server.Close()
	}
}

// startPACServerInternal runs a simple HTTP server to serve the PAC file from disk.
// If a PAC server is already running, it is stopped first to avoid port conflicts.
func startPACServerInternal() {
	debugLog("PAC", "Starting PAC server on port %d", Config.Network.PACHttpPort)
	// Stop any existing PAC server (avoids duplicate starts and port conflicts)
	stopPACServer()

	workDir := Config.Paths.WorkDir

	mux := http.NewServeMux()

	// Serving files from the work directory (where x_proxy.pac is written)
	fileServer := http.FileServer(http.Dir(workDir))

	// We wrap the file server to ensure correct Content-Type and no-cache
	mux.HandleFunc("/x_proxy.pac", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		fileServer.ServeHTTP(w, r)
	})

	// /healthz — статус туннеля/SOCKS/PAC (по анкете)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		host, active, _, failState := connState.GetAll()
		socks := checkSocks5(Config.Network.ProxyPort)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"host":%q,"active":%v,"socks":%v,"fail_state":%q,"pac_port":%d,"proxy_port":%d}`,
			host, active, socks, failState.String(), Config.Network.PACHttpPort, Config.Network.ProxyPort)
	})

	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", Config.Network.PACHttpPort),
		Handler: mux,
	}

	// Store reference for graceful shutdown
	pacServerMu.Lock()
	pacServer = server
	pacServerMu.Unlock()

	// Save PID for the HTTP server
	savePid(Config.TempFiles.PACServerPID, os.Getpid(), "PAC HTTP Server")

	go func() {
		defer func() {
			if r := recover(); r != nil {
				debugLog("PAC", "PANIC in ListenAndServe goroutine: %v", r)
				writeCrashLog(r)
			}
		}()
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			printWarn(fmt.Sprintf("PAC Server error: %v", err))
			debugLog("PAC", "PAC server error: %v", err)
		}
	}()
}
