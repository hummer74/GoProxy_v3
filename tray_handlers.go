package main

import (
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/getlantern/systray"
	windows "golang.org/x/sys/windows"
)

// handleHostClick handles clicks on host menu items
func handleHostClick(host HostConfig) {
	debugLog("HANDLER", "Host click: %s", host.Name)
	// Check if we're already connected to this host
	if connState.IsActive() && connState.GetHost() == host.Name {
		return
	}

	// Check if host is available before attempting connection
	if status, exists := hostStatusCache.Get(host.Name); exists && !status {
		logTunnelEvent("WARN", host.Name, "Attempted to connect to unavailable host")
		return
	}

	logTunnelEvent("INFO", host.Name, fmt.Sprintf("User clicked to connect to host: %s", host.HostName))

	// Show connecting state in tray
	iconData := loadIconData(color.RGBA{255, 255, 0, 255})
	if iconData != nil {
		systray.SetIcon(iconData)
	}
	systray.SetTitle(fmt.Sprintf("Connecting to %s...", host.Name))
	systray.SetTooltip(fmt.Sprintf("Connecting to %s...", host.Name))

	result := establishConnection(ConnectOptions{
		Hosts:              []HostConfig{host},
		OriginalHost:       host.Name,
		StopMonitoring:     true,
		KillExistingTunnel: true,
		EnableSystemProxy:  true,
		SaveLastHost:       true,
		StartMonitoring:    true,
		UpdateTray:         true,
	})

	if result == nil {
		// Connection failed — tray already shows failed state from pipeline
		iconData := loadIconData(color.RGBA{255, 255, 0, 255})
		if iconData != nil {
			systray.SetIcon(iconData)
		}
		systray.SetTitle("Connection failed")
		systray.SetTooltip("Failed to connect to host")
	}
}

// handleReverseHostClick handles clicks on reverse tunnel hosts (hosts with ProxyJump).
// Automatically resolves the ProxyJump chain and connects immediately in one click.
func handleReverseHostClick(host HostConfig) {
	debugLog("HANDLER", "Reverse host click: %s (ProxyJump: %s)", host.Name, host.ProxyJump)
	// Check if already connected to this host
	currentHostVal := connState.GetHost()
	if connState.IsActive() && strings.Contains(currentHostVal, " -> ") {
		chainParts := strings.Split(currentHostVal, " -> ")
		for _, part := range chainParts {
			if part == host.Name {
				return // Already connected via this host
			}
		}
	}
	if connState.IsActive() && currentHostVal == host.Name {
		return
	}

	// Check availability (reverse hosts check via their ProxyJump target)
	if status, exists := hostStatusCache.Get(host.Name); exists && !status {
		logTunnelEvent("WARN", host.Name, "Attempted to connect to unavailable reverse host")
		return
	}

	// Resolve ProxyJump target from parsed hosts
	var jumper *HostConfig
	for i := range allMenuHosts {
		if allMenuHosts[i].Name == host.ProxyJump {
			jumper = &allMenuHosts[i]
			break
		}
	}

	if jumper == nil {
		// ProxyJump target not found in config — fall back to single host connection
		logTunnelEvent("WARN", host.Name,
			fmt.Sprintf("ProxyJump target '%s' not found, falling back to direct connection", host.ProxyJump))
		handleHostClick(host)
		return
	}

	// Build auto-resolved chain: [jumper, target]
	chain := []HostConfig{*jumper, host}
	chainDisplay := fmt.Sprintf("%s -> %s", jumper.Name, host.Name)

	logTunnelEvent("INFO", chainDisplay,
		fmt.Sprintf("Auto-resolved reverse host connection: %s (via ProxyJump %s)", host.Name, host.ProxyJump))

	// Show connecting state in tray
	iconData := loadIconData(color.RGBA{255, 255, 0, 255})
	if iconData != nil {
		systray.SetIcon(iconData)
	}
	systray.SetTitle(fmt.Sprintf("Connecting to %s...", host.Name))
	systray.SetTooltip(fmt.Sprintf("Connecting via %s -> %s...", jumper.Name, host.Name))

	// Connect as chain with ProxyJump auto-resolution
	result := establishConnection(ConnectOptions{
		Hosts:              chain,
		IsChain:            true,
		OriginalHost:       chainDisplay,
		StopMonitoring:     true,
		KillExistingTunnel: true,
		EnableSystemProxy:  true,
		SaveLastHost:       true,
		StartMonitoring:    true,
		UpdateTray:         true,
		DisplayAlias:       host.Name,       // Show reverse host name in tray
		DisplayTooltip:     fmt.Sprintf("via %s -> %s:%s", jumper.Name, host.HostName, host.Port),
	})

	if result == nil {
		logTunnelEvent("ERROR", chainDisplay, "Reverse host connection failed")
		systray.SetTitle("Connection failed")
		systray.SetTooltip(fmt.Sprintf("Failed to connect to %s via %s", host.Name, jumper.Name))
	}
}

// Function hooks for tray/UI operations (used in tests and by monitoring)
var updateTrayStatusOnlineFn = updateTrayStatusOnline
var updateMenuStateFn = updateMenuState

// handleSmartFailover finds the fastest available host and connects to it.
// Now supports BOTH single hosts AND chains (including reverse tunnels).
// Returns true if failover succeeded and a new monitoring session was started
// (caller should exit its goroutine).
func handleSmartFailover(currentState *ProxyState) bool {
	debugLog("HANDLER", "Smart failover from: %s (isChain=%v)", currentState.Host, currentState.IsChain)
	if !Config.General.SmartFailover {
		return false
	}

	// Get all hosts from SSH config
	hosts := parseSSHConfig(Config.Paths.SSHConfig)
	if len(hosts) == 0 {
		return false
	}

	// Build list of available hosts (all groups, exclude current)
	var availableHosts []HostConfig
	currentHostName := currentState.Host

	// For chains, exclude ALL hosts that are part of the current chain
	excludeSet := make(map[string]bool)
	if currentState.IsChain && len(currentState.ChainHosts) > 0 {
		for _, name := range currentState.ChainHosts {
			excludeSet[name] = true
		}
	}
	excludeSet[currentHostName] = true

	for _, h := range hosts {
		if excludeSet[h.Name] {
			continue
		}
		// Exclude reverse hosts (they need a specific jumper)
		if isReverseHost(h) {
			continue
		}
		availableHosts = append(availableHosts, h)
	}

	if len(availableHosts) == 0 {
		logTunnelEvent("WARN", currentState.Host, "No alternative hosts available for smart failover")
		return false
	}

	// Find the fastest available host
	logTunnelEvent("INFO", currentState.Host,
		fmt.Sprintf("Looking for fastest among %d available hosts...", len(availableHosts)))
	fastestHost, responseTime := findFastestAvailableHost(availableHosts, Config.Paths.WorkDir)

	if fastestHost == nil {
		logTunnelEvent("WARN", currentState.Host, "No available hosts found for smart failover")
		return false
	}

	responseTimeSec := responseTime.Seconds()
	logTunnelEvent("INFO", currentState.Host,
		fmt.Sprintf("Fastest host found: %s (response time: %.2f seconds)", fastestHost.Name, responseTimeSec))

	// Determine original host name for recovery
	originalHost := currentState.OriginalHost
	if originalHost == "" {
		originalHost = currentState.Host
	}

	result := establishConnection(ConnectOptions{
		Hosts:              []HostConfig{*fastestHost},
		OriginalHost:       originalHost,
		IsFailoverActive:   true,
		FailoverStart:      time.Now().Format(time.RFC3339),
		StopMonitoring:     true,
		KillExistingTunnel: true,
		EnableSystemProxy:  false, // proxy already set, we're switching
		SaveLastHost:       false,
		StartMonitoring:    true,
		UpdateTray:         true,
		DisplayAlias:       fastestHost.Name,
		DisplayTooltip:     fastestHost.HostName,
	})

	if result != nil {
		logTunnelEvent("OK", currentState.Host,
			fmt.Sprintf("Smart failover completed: switched to %s", fastestHost.Name))
		return true
	}

	return false
}

// checkAndReturnToOriginalHost checks if the original host is available
// and returns to it when in failover mode.
// Used by the old Recovery logic path (via function pointer).
func checkAndReturnToOriginalHost(currentState *ProxyState) bool {
	if !Config.General.ReturnToOriginalHost || !currentState.IsFailoverActive {
		return false
	}

	// Find the original host configuration
	hosts := parseSSHConfig(Config.Paths.SSHConfig)
	if len(hosts) == 0 {
		return false
	}

	var originalHostConfig *HostConfig
	for _, host := range hosts {
		if host.Name == currentState.OriginalHost {
			originalHostConfig = &host
			break
		}
	}

	if originalHostConfig == nil {
		logTunnelEvent("WARN", currentState.Host, "Original host configuration not found")
		return false
	}

	// Check original host availability
	logTunnelEvent("INFO", currentState.Host,
		fmt.Sprintf("Checking original host availability: %s", currentState.OriginalHost))

	available, responseTime := checkSSHConnectionWithTime(*originalHostConfig, Config.Paths.WorkDir)

	if !available {
		return false
	}

	responseTimeSec := responseTime.Seconds()
	logTunnelEvent("INFO", currentState.Host,
		fmt.Sprintf("Original host %s is available (response time: %.2f seconds)",
			currentState.OriginalHost, responseTimeSec))

	result := establishConnection(ConnectOptions{
		Hosts:              []HostConfig{*originalHostConfig},
		OriginalHost:       originalHostConfig.Name,
		StopMonitoring:     true,
		KillExistingTunnel: true,
		EnableSystemProxy:  true,
		SaveLastHost:       true,
		StartMonitoring:    true,
		UpdateTray:         true,
		DisplayAlias:       originalHostConfig.Name,
		DisplayTooltip:     originalHostConfig.HostName,
	})

	if result != nil {
		logTunnelEvent("OK", currentState.Host,
			fmt.Sprintf("Returned to original host: %s", originalHostConfig.Name))
		return true
	}

	return false
}

// handleConnectChain handles the "Connect Chain" button click
func handleConnectChain() {
	chain := getChainBuilderCopy()
	debugLog("HANDLER", "Connect chain: %d hosts", len(chain))
	if len(chain) == 0 {
		return
	}

	// Snapshot and clear immediately to prevent TOCTOU:
	// another click could modify the builder while we're connecting.
	clearChainBuilder()
	updateChainBuilderUI()

	// Build chain display string for logging
	var names []string
	for _, h := range chain {
		names = append(names, h.Name)
	}
	chainDisplay := strings.Join(names, " -> ")

	logTunnelEvent("INFO", chainDisplay, fmt.Sprintf("User requested chain connection: %s", chainDisplay))

	if len(chain) == 1 {
		// Single host — use direct connection
		handleHostClick(chain[0])
	} else {
		// Multiple hosts — use chain connection via pipeline
		result := establishConnection(ConnectOptions{
			Hosts:              chain,
			IsChain:            true,
			OriginalHost:       chainDisplay,
			StopMonitoring:     true,
			KillExistingTunnel: true,
			EnableSystemProxy:  true,
			SaveLastHost:       true,
			StartMonitoring:    true,
			UpdateTray:         true,
			DisplayAlias:       "Chain",
			DisplayTooltip:     chainDisplay,
		})

		if result == nil {
			logTunnelEvent("ERROR", chainDisplay, "Chain connection failed")
			updateTrayStatusReconnecting("Chain", chainDisplay)
		}
	}
}

// handleClearChain handles the "Clear Selection" button click
func handleClearChain() {
	chainCopy := getChainBuilderCopy()
	if len(chainCopy) == 0 {
		return
	}

	var names []string
	for _, h := range chainCopy {
		names = append(names, h.Name)
	}

	logTunnelEvent("INFO", strings.Join(names, ", "), "User cleared chain selection")

	clearChainBuilder()
	updateChainBuilderUI()
}

// handleKillProxy handles the Kill Proxy menu item
func handleKillProxy() {
	debugLog("HANDLER", "Kill proxy")
	logTunnelEvent("INFO", connState.GetHost(), "User requested to kill proxy")

	stopMonitoring()
	disableSystemProxy()
	killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")

	iconData := loadIconData(color.RGBA{255, 0, 0, 255})
	if iconData != nil {
		systray.SetIcon(iconData)
	}
	systray.SetTitle("Proxy Killed")
	systray.SetTooltip("Proxy has been killed. Select a host to connect.")

	connState.SetDisconnected()
	connState.SetFailState(StateNormal)
	updateMenuState()
}

// handleExit handles the Exit menu item - performs full cleanup inline
func handleExit() {
	debugLog("HANDLER", "Exit")
	logTunnelEvent("INFO", connState.GetHost(), "User requested exit")

	stopMonitoring()

	// Kill SSH tunnel
	killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")

	// Stop PAC server
	stopPACServer()

	// Disable system proxy
	disableSystemProxy()

	// Clean up all temp files
	cleanupTempFiles()

	// Stop tickers
	if menuUpdateTicker != nil {
		menuUpdateTicker.Stop()
	}
	if hostsCheckTicker != nil {
		hostsCheckTicker.Stop()
	}

	time.Sleep(500 * time.Millisecond)
	systray.Quit()
}

// cleanupTempFiles removes all GoProxy temp files
func cleanupTempFiles() {
	debugLog("HANDLER", "Cleaning up temp files")
	files := []string{
		Config.TempFiles.PACFile,
		Config.TempFiles.StateFile,
		Config.TempFiles.SSHTunnelPID,
		Config.TempFiles.TrayPID,
		Config.TempFiles.PACServerPID,
		Config.TempFiles.StopFlag,
	}
	for _, f := range files {
		os.Remove(f)
	}
}

// handleFatalError handles fatal error state with timeout.
// Transitions to Error/Halt — shows red icon, disables proxy, kills tunnel.
func handleFatalError(remoteAlias, displayHost string) {
	logTunnelEvent("ERROR", remoteAlias, "Fatal error - entering Error/Halt state")

	iconData := loadIconData(color.RGBA{255, 0, 0, 255})
	if iconData != nil {
		systray.SetIcon(iconData)
	}
	systray.SetTitle("FATAL ERROR")
	systray.SetTooltip(fmt.Sprintf("%s: ERROR/HALT\n%s\nManual intervention required", remoteAlias, displayHost))

	disableSystemProxy()
	killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
}

// attemptTunnelRestart attempts to restart SSH tunnel (single attempt)
// This is a special case: it reuses the existing SSHCommand from state
// and waits for the tunnel to come up, without going through the full pipeline.
func attemptTunnelRestart(state *ProxyState) bool {
	killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
	time.Sleep(1 * time.Second)

	sshKeyPass := loadSSHKeyPassphrase()

	if !ensureSSHAgent(state.KeyPath, sshKeyPass) {
		logTunnelEvent("WARN", state.Host, "SSH agent SSH-KEY loading failed during restart")
	}

	cmd := exec.Command(state.SSHCommand[0], state.SSHCommand[1:]...)
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}

	if err := cmd.Start(); err != nil {
		logTunnelEvent("ERROR", state.Host, fmt.Sprintf("Failed to start SSH process during restart: %v", err))
		return false
	}

	savePid(Config.TempFiles.SSHTunnelPID, cmd.Process.Pid, state.Host)

	// Wait for tunnel to become available (max 10 seconds) using Ticker
	const restartTimeout = 10 * time.Second
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	timeoutCh := time.After(restartTimeout)

	for {
		select {
		case <-timeoutCh:
			logTunnelEvent("ERROR", state.Host, fmt.Sprintf("Tunnel restart timeout (%v)", restartTimeout))
			killPid(cmd.Process.Pid)
			os.Remove(Config.TempFiles.SSHTunnelPID)
			return false
		case <-ticker.C:
			if checkProxyConnectivity() {
				logTunnelEvent("OK", state.Host, "Tunnel restart successful")
				return true
			}
		}
	}
}
