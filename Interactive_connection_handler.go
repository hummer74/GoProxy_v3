// Interactive_connection_handler.go
package main

import (
        "fmt"
        "strings"
        "time"
)

// Function hooks for testing
var startSSHTunnelFn = startSSHTunnel
var startSSHTunnelWithRetriesFn = startSSHTunnelWithRetries
var launchTrayAndExitFn = LaunchTrayAndExit
var startPACServerFn = startPACServer
var setSystemProxyFn = setSystemProxy
var ensureSSHAgentFn = ensureSSHAgent
var saveStateFn = SaveState
var saveLastHostFn = SaveLastHost
var startMonitoringFn = startMonitoring
var stopMonitoringFn = stopMonitoring
var killProcessByFileFn = killProcessByFile

// handleConnectionInteractive establishes connection to a single host from interactive mode
func handleConnectionInteractive(targetHost HostConfig) {
        fmt.Print("\033[H\033[2J")
        fmt.Printf("Connecting to: %s...\n", targetHost.Name)

        result := establishConnection(ConnectOptions{
                Hosts:              []HostConfig{targetHost},
                OriginalHost:       targetHost.Name,
                StopMonitoring:     true,
                KillExistingTunnel: true,
                EnableSystemProxy:  true,
                SaveLastHost:       true,
                StartMonitoring:    true,
                InteractiveMode:    true, // skip tray UI updates
        })

        if result != nil {
                fmt.Printf("\n\033[32mSuccessfully connected to %s\033[0m\n", targetHost.Name)
                time.Sleep(1 * time.Second)
                launchTrayAndExitFn()
        }
}

// handleChainConnectionInteractive establishes multi-hop SSH tunnel from interactive mode
func handleChainConnectionInteractive(chainHosts []HostConfig) {
        if len(chainHosts) == 0 {
                return
        }

        fmt.Print("\033[H\033[2J")
        var names []string
        for _, h := range chainHosts {
                names = append(names, h.Name)
        }
        chainDisplay := strings.Join(names, " -> ")
        fmt.Printf("Establishing Chain: %s...\n", chainDisplay)

        result := establishConnection(ConnectOptions{
                Hosts:              chainHosts,
                IsChain:            true,
                OriginalHost:       chainDisplay,
                KillExistingTunnel: true,
                EnableSystemProxy:  true,
                SaveLastHost:       true,
                StartMonitoring:    true,
                InteractiveMode:    true,
                DisplayAlias:       "Chain",
                DisplayTooltip:     chainDisplay,
        })

        if result != nil {
                fmt.Printf("\n\033[32mChain tunnel is ACTIVE: %s\033[0m\n", chainDisplay)
                time.Sleep(1 * time.Second)
                launchTrayAndExitFn()
        }
}
