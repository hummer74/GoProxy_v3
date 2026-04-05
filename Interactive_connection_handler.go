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

	stopMonitoringFn() // Stop old monitoring
	killProcessByFileFn(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")

	workDir := Config.Paths.WorkDir
	sshKeyPass := loadSSHKeyPassphrase()
	sshKeyPath := resolveSSHKeyPath(workDir, targetHost.IdentityFile)

	ensureSSHAgentFn(sshKeyPath, sshKeyPass)

	sshCmd := buildSSHCommand([]HostConfig{targetHost}, sshKeyPath)

	if startSSHTunnelFn(sshCmd) {
		state := ProxyState{
			IsChain:          false,
			Host:             targetHost.Name,
			OriginalHost:     targetHost.Name,
			IsFailoverActive: false,
			ProxyPort:        Config.Network.ProxyPort,
			KeyPath:          sshKeyPath,
			SSHCommand:       sshCmd,
			RemoteHost:       targetHost.HostName,
		}
		SaveState(state)
		SaveLastHost(targetHost.Name)

		startPACServerFn()
		pacURL := fmt.Sprintf("http://127.0.0.1:%d/x_proxy.pac", Config.Network.PACHttpPort)
		setSystemProxyFn(pacURL)

		currentHost = targetHost.Name
		isTunnelActive = true
		tunnelStartTime = time.Now()

		// REMOVED: updateMenuState() and updateTrayStatusOnline - these are for tray mode only
		// Start monitoring in background (will be taken over by tray process later)
		go startMonitoringFn(&state)

		fmt.Printf("\n\033[32mSuccessfully connected to %s\033[0m\n", targetHost.Name)
		time.Sleep(1 * time.Second)

		// Switch to tray mode and exit interactive process
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

	stopMonitoringFn()
	killProcessByFileFn(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")

	workDir := Config.Paths.WorkDir
	sshKeyPass := loadSSHKeyPassphrase()
	sshKeyPath := resolveSSHKeyPath(workDir, chainHosts[0].IdentityFile)
	ensureSSHAgentFn(sshKeyPath, sshKeyPass)

	sshCmd := buildSSHCommand(chainHosts, sshKeyPath)

	if startSSHTunnelWithRetriesFn(sshCmd, len(chainHosts)) {
		state := ProxyState{
			IsChain:          true,
			Host:             chainDisplay,
			OriginalHost:     chainDisplay,
			IsFailoverActive: false,
			ChainHosts:       names,
			ProxyPort:        Config.Network.ProxyPort,
			KeyPath:          sshKeyPath,
			SSHCommand:       sshCmd,
			RemoteHost:       chainHosts[len(chainHosts)-1].HostName,
		}
		SaveState(state)
		SaveLastHost(strings.Join(names, "|"))

		startPACServerFn()
		pacURL := fmt.Sprintf("http://127.0.0.1:%d/x_proxy.pac", Config.Network.PACHttpPort)
		setSystemProxyFn(pacURL)

		currentHost = chainDisplay
		isTunnelActive = true
		tunnelStartTime = time.Now()

		// REMOVED: updateMenuState() and updateTrayStatusOnline - these are for tray mode only
		// Start monitoring in background (will be taken over by tray process later)
		go startMonitoringFn(&state)

		fmt.Printf("\n\033[32mChain tunnel is ACTIVE: %s\033[0m\n", chainDisplay)
		time.Sleep(1 * time.Second)

		// Switch to tray mode and exit interactive process
		launchTrayAndExitFn()
	}
}
