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

// Function hooks for tray/UI operations (used in tests and by monitoring)
var updateTrayStatusOnlineFn = updateTrayStatusOnline
var updateMenuStateFn = updateMenuState

// handleSmartFailover выполняет умное переключение на самый быстрый доступный хост
func handleSmartFailover(currentState *ProxyState) bool {
    if !Config.General.SmartFailover || currentState.IsChain {
        return false
    }

    // Получаем все хосты из основной группы
    hosts := parseSSHConfig(Config.Paths.SSHConfig)
    if len(hosts) == 0 {
        return false
    }

    // Получаем первую группу (как в основном меню)
    firstGroup := hosts[0].Group
    var firstGroupHosts []HostConfig
    for _, h := range hosts {
        if h.Group == firstGroup {
            firstGroupHosts = append(firstGroupHosts, h)
        }
    }

    // Исключаем текущий хост из поиска
    var availableHosts []HostConfig
    for _, host := range firstGroupHosts {
        if host.Name != currentState.OriginalHost && host.Name != currentState.Host {
            availableHosts = append(availableHosts, host)
        }
    }

    if len(availableHosts) == 0 {
        logTunnelEvent("WARN", currentState.Host, "No alternative hosts available for smart failover")
        return false
    }

    // Ищем самый быстрый доступный хост
    logTunnelEvent("INFO", currentState.Host, "Looking for fastest available host...")
    fastestHost, responseTime := findFastestAvailableHost(availableHosts, Config.Paths.WorkDir)

    if fastestHost == nil {
        logTunnelEvent("WARN", currentState.Host, "No available hosts found for smart failover")
        return false
    }

    responseTimeSec := responseTime.Seconds()
    logTunnelEvent("INFO", currentState.Host,
        fmt.Sprintf("Fastest host found: %s (response time: %.2f seconds)", fastestHost.Name, responseTimeSec))

    result := establishConnection(ConnectOptions{
        Hosts:              []HostConfig{*fastestHost},
        OriginalHost:       currentState.OriginalHost,
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

// checkAndReturnToOriginalHost проверяет оригинальный хост и возвращается к нему если доступен
func checkAndReturnToOriginalHost(currentState *ProxyState) bool {
    if !Config.General.ReturnToOriginalHost || !currentState.IsFailoverActive {
        return false
    }

    // Находим конфигурацию оригинального хоста
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

    // Проверяем доступность оригинального хоста
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
    updateMenuState()
}

// handleExit handles the Exit menu item - performs full cleanup using stop mode
func handleExit() {
    logTunnelEvent("INFO", connState.GetHost(), "User requested exit")

    stopMonitoring()
    os.WriteFile(Config.TempFiles.StopFlag, []byte("stop"), 0644)

    execPath, err := os.Executable()
    if err != nil {
        cleanupOnExitFallback()
        return
    }

    cmd := exec.Command(execPath, "-stop")
    cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}

    if err := cmd.Start(); err != nil {
        cleanupOnExitFallback()
    }

    if menuUpdateTicker != nil {
        menuUpdateTicker.Stop()
    }
    if hostsCheckTicker != nil {
        hostsCheckTicker.Stop()
    }

    systray.Quit()
}

// cleanupOnExitFallback is used when we can't start the stop process
func cleanupOnExitFallback() {
    disableSystemProxy()

    killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
    stopPACServer()

    time.Sleep(2 * time.Second)

    os.Remove(Config.TempFiles.TrayPID)

    if menuUpdateTicker != nil {
        menuUpdateTicker.Stop()
    }
    if hostsCheckTicker != nil {
        hostsCheckTicker.Stop()
    }

    systray.Quit()
}

// handleFatalError handles fatal error state with 60-minute timeout
func handleFatalError(remoteAlias, displayHost string) {
    logTunnelEvent("ERROR", remoteAlias, "Fatal error - 60 minute reconnect timeout")

    iconData := loadIconData(color.RGBA{255, 0, 0, 255})
    if iconData != nil {
        systray.SetIcon(iconData)
    }
    systray.SetTitle("FATAL ERROR")
    systray.SetTooltip(fmt.Sprintf("%s: RECONNECT_TIMEOUT (60m)\n%s", remoteAlias, displayHost))

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
