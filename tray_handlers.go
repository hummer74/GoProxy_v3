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
    if isTunnelActive && currentHost == host.Name {
        // Already connected to this host, ignore click
        return
    }

    // Check if host is available before attempting connection
    if status, exists := hostStatusCache.Get(host.Name); exists && !status {
        // Host is marked as unavailable, don't try to connect
        logTunnelEvent("WARN", host.Name, "Attempted to connect to unavailable host")
        return
    }

    logTunnelEvent("INFO", host.Name, fmt.Sprintf("User clicked to connect to host: %s", host.HostName))

    // Stop current monitoring if active
    stopMonitoring()

    // Kill any existing tunnel
    killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
    time.Sleep(500 * time.Millisecond) // Short delay to ensure process is killed

    // Connect to the selected host
    iconData := loadIconData(color.RGBA{255, 255, 0, 255})
    if iconData != nil {
        systray.SetIcon(iconData)
    }
    systray.SetTitle(fmt.Sprintf("Connecting to %s...", host.Name))
    systray.SetTooltip(fmt.Sprintf("Connecting to %s...", host.Name))

    // Load SSH-KEY-PASS from file
    sshKeyPass := loadSSHKeyPassphrase()

    // Resolve SSH-KEY path
    sshKeyPath := resolveSSHKeyPath(Config.Paths.WorkDir, host.IdentityFile)

    // Load SSH-KEY into agent with SSH-KEY-PASS
    ensureSSHAgent(sshKeyPath, sshKeyPass)

    sshCmd := buildSSHCommand([]HostConfig{host}, sshKeyPath)

    if startSSHTunnel(sshCmd) {
        state := ProxyState{
            IsChain:          false,
            Host:             host.Name,
            OriginalHost:     host.Name, // Сохраняем как оригинальный хост
            IsFailoverActive: false,     // Не failover, пользователь выбрал вручную
            ProxyPort:        Config.Network.ProxyPort,
            KeyPath:          sshKeyPath,
            SSHCommand:       sshCmd,
            RemoteHost:       host.HostName,
        }
        SaveState(state)
        SaveLastHost(host.Name)
        startPACServer()
        pacURL := fmt.Sprintf("http://127.0.0.1:%d/x_proxy.pac", Config.Network.PACHttpPort)
        setSystemProxy(pacURL)

        // Update state
        currentHost = host.Name
        isTunnelActive = true
        tunnelStartTime = time.Now()

        // Update menu
        updateMenuState()

        // Update tray icon and title
        updateTrayStatusOnline(host.Name, host.HostName)

        // Start monitoring for this connection
        go startMonitoring(&state)
    } else {
        logTunnelEvent("ERROR", host.Name, "Connection failed")
        // Reset icon to yellow
        iconData := loadIconData(color.RGBA{255, 255, 0, 255})
        if iconData != nil {
            systray.SetIcon(iconData)
        }
        systray.SetTitle("Connection failed")
        systray.SetTooltip("Failed to connect to host")
    }
}

// Function hooks for tray/UI operations
var updateTrayStatusOnlineFn = updateTrayStatusOnline
var updateMenuStateFn = updateMenuState

// handleSmartFailover выполняет умное переключение на самый быстрый доступный хост
func handleSmartFailover(currentState *ProxyState) bool {
    if !Config.General.SmartFailover || currentState.IsChain {
        // Smart failover отключен или это цепочка - не используем умное переключение
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

    // Преобразуем наносекунды в секунды для логирования
    responseTimeSec := responseTime.Seconds()
    logTunnelEvent("INFO", currentState.Host,
        fmt.Sprintf("Fastest host found: %s (response time: %.2f seconds)", fastestHost.Name, responseTimeSec))

    // Подключаемся к найденному хосту
    sshKeyPass := loadSSHKeyPassphrase()
    sshKeyPath := resolveSSHKeyPath(Config.Paths.WorkDir, fastestHost.IdentityFile)

    // Load SSH-KEY into agent with SSH-KEY-PASS
    ensureSSHAgent(sshKeyPath, sshKeyPass)

    sshCmd := buildSSHCommand([]HostConfig{*fastestHost}, sshKeyPath)

    // Останавливаем текущий мониторинг
    stopMonitoring()

    // Убиваем текущий туннель
    killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
    time.Sleep(500 * time.Millisecond)

    if startSSHTunnelFn(sshCmd) {
        // Обновляем состояние
        newState := ProxyState{
            IsChain:          false,
            Host:             fastestHost.Name,
            OriginalHost:     currentState.OriginalHost, // Сохраняем оригинальный хост
            IsFailoverActive: true,                      // Помечаем как failover
            FailoverStart:    time.Now().Format(time.RFC3339),
            ProxyPort:        Config.Network.ProxyPort,
            KeyPath:          sshKeyPath,
            SSHCommand:       sshCmd,
            RemoteHost:       fastestHost.HostName,
        }

        SaveState(newState)

        // Обновляем глобальное состояние
        currentHost = fastestHost.Name
        isTunnelActive = true
        tunnelStartTime = time.Now()

        // Обновляем меню
        updateMenuStateFn()

        // Обновляем иконку и заголовок в трее
        updateTrayStatusOnlineFn(fastestHost.Name, fastestHost.HostName+" (Failover)")

        // Запускаем мониторинг для нового подключения
        go startMonitoring(&newState)

        logTunnelEvent("OK", currentState.Host,
            fmt.Sprintf("Smart failover completed: switched to %s", fastestHost.Name))
        return true
    }

    return false
}

// checkAndReturnToOriginalHost проверяет оригинальный хост и возвращается к нему если доступен
func checkAndReturnToOriginalHost(currentState *ProxyState) bool {
    if !Config.General.ReturnToOriginalHost || !currentState.IsFailoverActive {
        // Возврат к оригинальному хосту отключен или мы не в режиме failover
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

    if available {
        // Преобразуем наносекунды в секунды для логирования
        responseTimeSec := responseTime.Seconds()
        logTunnelEvent("INFO", currentState.Host,
            fmt.Sprintf("Original host %s is available (response time: %.2f seconds)",
                currentState.OriginalHost, responseTimeSec))

        // Подключаемся обратно к оригинальному хосту
        sshKeyPass := loadSSHKeyPassphrase()
        sshKeyPath := resolveSSHKeyPath(Config.Paths.WorkDir, originalHostConfig.IdentityFile)

        // Load SSH-KEY into agent with SSH-KEY-PASS
        ensureSSHAgent(sshKeyPath, sshKeyPass)

        sshCmd := buildSSHCommand([]HostConfig{*originalHostConfig}, sshKeyPath)

        // Останавливаем текущий мониторинг
        stopMonitoring()

        // Убиваем текущий туннель
        killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
        time.Sleep(500 * time.Millisecond)

        if startSSHTunnel(sshCmd) {
            // Обновляем состояние
            newState := ProxyState{
                IsChain:          false,
                Host:             originalHostConfig.Name,
                OriginalHost:     originalHostConfig.Name, // Обновляем оригинальный хост
                IsFailoverActive: false,                   // Больше не в режиме failover
                ProxyPort:        Config.Network.ProxyPort,
                KeyPath:          sshKeyPath,
                SSHCommand:       sshCmd,
                RemoteHost:       originalHostConfig.HostName,
            }

            SaveState(newState)
            SaveLastHost(originalHostConfig.Name)

            // Обновляем глобальное состояние
            currentHost = originalHostConfig.Name
            isTunnelActive = true
            tunnelStartTime = time.Now()

            // Обновляем меню
            updateMenuState()

            // Обновляем иконку и заголовок в трее
            updateTrayStatusOnline(originalHostConfig.Name, originalHostConfig.HostName)

            // Запускаем мониторинг для нового подключения
            go startMonitoring(&newState)

            logTunnelEvent("OK", currentState.Host,
                fmt.Sprintf("Returned to original host: %s", originalHostConfig.Name))
            return true
        }
    }

    return false
}

// handleConnectChain handles the "Connect Chain" button click
func handleConnectChain() {
    chain := getChainBuilderCopy()
    if len(chain) == 0 {
        return
    }

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
        // Multiple hosts — use chain connection
        handleChainConnection(chain, true) // true = manual (user clicked Connect)
    }

    // Clear chain builder after connection attempt
    clearChainBuilder()
    updateChainBuilderUI()
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
    logTunnelEvent("INFO", currentHost, "User requested to kill proxy")

    // Stop monitoring first
    stopMonitoring()

    // Disable system proxy
    disableSystemProxy()

    // Kill all tunnel processes
    killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")

    // Set red icon
    iconData := loadIconData(color.RGBA{255, 0, 0, 255})
    if iconData != nil {
        systray.SetIcon(iconData)
    }
    systray.SetTitle("Proxy Killed")
    systray.SetTooltip("Proxy has been killed. Select a host to connect.")

    // Reset state
    currentHost = ""
    isTunnelActive = false
    tunnelStartTime = time.Time{}

    // Update menu - all hosts should be clickable now
    updateMenuState()
}

// handleExit handles the Exit menu item - performs full cleanup using stop mode
func handleExit() {
    logTunnelEvent("INFO", currentHost, "User requested exit")

    // Stop monitoring first
    stopMonitoring()

    // Create stop flag for Tray (this will be read by monitoring loops)
    os.WriteFile(Config.TempFiles.StopFlag, []byte("stop"), 0644)

    // Get current executable path
    execPath, err := os.Executable()
    if err != nil {
        // Fallback: try to clean up manually
        cleanupOnExitFallback()
        return
    }

    // Start a new process in stop mode to ensure proper cleanup
    cmd := exec.Command(execPath, "-stop")
    cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}

    // Start the process (don't wait for it to finish)
    if err := cmd.Start(); err != nil {
        // If we can't start the stop process, fall back to manual cleanup
        cleanupOnExitFallback()
    }

    // Stop menu update ticker
    if menuUpdateTicker != nil {
        menuUpdateTicker.Stop()
    }

    // Stop hosts check ticker
    if hostsCheckTicker != nil {
        hostsCheckTicker.Stop()
    }

    // Quit tray immediately - stop process will handle cleanup
    systray.Quit()
}

// cleanupOnExitFallback is used when we can't start the stop process
func cleanupOnExitFallback() {
    // Disable system proxy
    disableSystemProxy()

    // Stop all processes
    killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
    killProcessByFile(Config.TempFiles.PACServerPID, "PAC HTTP Server")

    // Wait a bit for processes to exit
    time.Sleep(2 * time.Second)

    // Clean up PID file
    os.Remove(Config.TempFiles.TrayPID)

    // Stop menu update ticker
    if menuUpdateTicker != nil {
        menuUpdateTicker.Stop()
    }

    // Stop hosts check ticker
    if hostsCheckTicker != nil {
        hostsCheckTicker.Stop()
    }

    // Quit tray
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

    // Disable system proxy
    disableSystemProxy()

    // Kill any existing tunnel process
    killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
}

// attemptTunnelRestart attempts to restart SSH tunnel (single attempt)
func attemptTunnelRestart(state *ProxyState) bool {
    // Kill any existing tunnel process
    killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
    time.Sleep(1 * time.Second)

    // Load SSH-KEY-PASS from file
    sshKeyPass := loadSSHKeyPassphrase()

    // Ensure SSH agent has the SSH-KEY
    if !ensureSSHAgent(state.KeyPath, sshKeyPass) {
        logTunnelEvent("WARN", state.Host, "SSH agent SSH-KEY loading failed during restart")
    }

    // Build and start SSH command
    cmd := exec.Command(state.SSHCommand[0], state.SSHCommand[1:]...)
    cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}

    if err := cmd.Start(); err != nil {
        logTunnelEvent("ERROR", state.Host, fmt.Sprintf("Failed to start SSH process during restart: %v", err))
        return false
    }

    // Save PID
    savePid(Config.TempFiles.SSHTunnelPID, cmd.Process.Pid, state.Host)

    // Wait for tunnel to become available (max 10 seconds)
    for i := 0; i < 10; i++ {
        time.Sleep(1 * time.Second)
        if checkProxyConnectivity() {
            logTunnelEvent("OK", state.Host, "Tunnel restart successful")
            return true
        }
    }

    // Tunnel didn't respond in time
    logTunnelEvent("ERROR", state.Host, "Tunnel restart timeout (10 seconds)")
    killPid(cmd.Process.Pid)
    os.Remove(Config.TempFiles.SSHTunnelPID)
    return false
}
