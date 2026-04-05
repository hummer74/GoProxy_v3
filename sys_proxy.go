package main

import (
	"fmt"
	"net/http"
	"os"

	windows "golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// CurrentPACContent holds the latest generated PAC logic for reference
var CurrentPACContent string

// setSystemProxy configures Windows Internet Settings for PAC URL
func setSystemProxy(pacURL string) bool {
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

// disableSystemProxy clears PAC and disables proxy in Windows Internet Settings
func disableSystemProxy() {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, windows.KEY_SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	
	_ = k.SetStringValue("AutoConfigURL", "")
	_ = k.SetDWordValue("ProxyEnable", 0)
}

// startPACServerInternal runs a simple HTTP server to serve the PAC file from disk
func startPACServerInternal() {
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

	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", Config.Network.PACHttpPort),
		Handler: mux,
	}

	// Save PID for the HTTP server
	savePid(Config.TempFiles.PACServerPID, os.Getpid(), "PAC HTTP Server")

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			printWarn(fmt.Sprintf("PAC Server error: %v", err))
		}
	}()
}