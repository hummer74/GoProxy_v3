// tray_monitor.go
package main

import (
        "fmt"
        "os"
        "strings"
        "time"
)

var checkAndReturnToOriginalHostFunc func(*ProxyState) bool
var checkProxyConnectivityFunc func() bool
var checkInternetFunc func() bool
var attemptTunnelRestartFunc func(*ProxyState) bool

func init() {
        checkAndReturnToOriginalHostFunc = checkAndReturnToOriginalHost
        checkProxyConnectivityFunc = checkProxyConnectivity
        checkInternetFunc = checkInternet
        attemptTunnelRestartFunc = attemptTunnelRestart
}

// ---------------------------------------------------------------------------
// monitoringConfig — holds all timing and mutable state for a single
// monitoring session, replacing 7 loose local variables.
// ---------------------------------------------------------------------------

type monitoringConfig struct {
        // Timing configuration (resolved once from Config at session start)
        socksCheckInterval    time.Duration
        internetCheckDelay    time.Duration
        internetCheckRetry    time.Duration
        reconnectDelay        time.Duration
        maxReconnectTime      time.Duration
        origHostCheckInterval time.Duration

        // Mutable session state
        isReconnecting        bool
        isFatalError          bool
        reconnectStartTime    time.Time
        lastInternetCheck     time.Time
        lastReconnectAttempt  time.Time
        lastSocksCheck        time.Time
        lastOrigHostCheck     time.Time
        networkAvailable      bool
        reconnectAttempts     int
}

func newMonitoringConfig() *monitoringConfig {
        return &monitoringConfig{
                socksCheckInterval:    time.Duration(Config.Network.SocksCheckInterval) * time.Second,
                internetCheckDelay:    time.Duration(Config.Network.InternetCheckDelay) * time.Second,
                internetCheckRetry:    time.Duration(Config.Network.InternetCheckRetry) * time.Second,
                reconnectDelay:        time.Duration(Config.Network.ReconnectAttemptDelay) * time.Second,
                maxReconnectTime:      time.Duration(Config.Network.MaxReconnectTime) * time.Second,
                origHostCheckInterval: time.Duration(Config.General.OriginalHostCheck) * time.Second,
        }
}

// ---------------------------------------------------------------------------
// Tray-display helpers — eliminate the repeated "if chain / else" pattern
// that appeared 6 times in the original startMonitoring().
// ---------------------------------------------------------------------------

// aliasForState returns the short label used in tray title/tooltip for a connection.
func aliasForState(state *ProxyState) string {
        if state.IsChain {
                return "Chain"
        }
        return state.Host
}

// remoteForState returns the remote-host description for tray tooltips.
func remoteForState(state *ProxyState) string {
        if state.IsChain {
                return state.Host
        }
        return state.RemoteHost
}

// ---------------------------------------------------------------------------
// State handlers — one method per monitoring state
// ---------------------------------------------------------------------------

// checkOriginalHostReturn periodically checks if the original host is
// available and attempts to return when in failover mode.
// Returns true if the caller should exit monitoring (successfully returned).
func (mc *monitoringConfig) checkOriginalHostReturn(state *ProxyState) bool {
        if !Config.General.ReturnToOriginalHost || !state.IsFailoverActive {
                return false
        }
        if mc.origHostCheckInterval == 0 || time.Since(mc.lastOrigHostCheck) < mc.origHostCheckInterval {
                return false
        }
        mc.lastOrigHostCheck = time.Now()

        return checkAndReturnToOriginalHostFunc(state)
}

// handleFatalErrorState periodically pings SOCKS5; if the tunnel recovers on
// its own the state resets to normal.
func (mc *monitoringConfig) handleFatalErrorState(state *ProxyState) {
        if time.Since(mc.lastSocksCheck) < mc.socksCheckInterval {
                return
        }
        mc.lastSocksCheck = time.Now()

        if checkProxyConnectivityFunc() {
                mc.isFatalError = false
                mc.isReconnecting = false
                mc.reconnectAttempts = 0
                connState.SetStartTime(time.Now())
                connState.SetActive(true)
                logTunnelEvent("OK", state.Host, "Tunnel recovered from fatal error state")
                updateTrayStatusOnline(aliasForState(state), remoteForState(state))
                updateMenuState()
        }
}

// handleNormalState checks SOCKS5 periodically.  When the tunnel drops it
// transitions into reconnection state and optionally triggers smart failover.
func (mc *monitoringConfig) handleNormalState(state *ProxyState) {
        if time.Since(mc.lastSocksCheck) < mc.socksCheckInterval {
                return
        }
        mc.lastSocksCheck = time.Now()

        if checkProxyConnectivityFunc() {
                // Tunnel is online — if we were reconnecting, mark as recovered
                if mc.isReconnecting {
                        mc.isReconnecting = false
                        mc.reconnectAttempts = 0
                        connState.SetStartTime(time.Now())
                        connState.SetActive(true)
                        logTunnelEvent("OK", state.Host, "Tunnel reconnected successfully")
                        updateMenuState()
                }
                return
        }

        // Tunnel went offline
        if mc.isReconnecting {
                return // already handling
        }

        mc.isReconnecting = true
        mc.reconnectStartTime = time.Now()
        mc.lastInternetCheck = time.Time{}
        mc.lastReconnectAttempt = time.Time{}
        mc.reconnectAttempts = 0
        connState.SetActive(false)
        logTunnelEvent("ERROR", state.Host, "Tunnel lost connection")
        updateMenuState()

        // Try smart failover if enabled (single-host only)
        if Config.General.SmartFailover && !state.IsChain {
                if handleSmartFailover(state) {
                        return // switched to another host — this monitoring session ends
                }
        }

        updateTrayStatusReconnecting(aliasForState(state), remoteForState(state))
        disableSystemProxy()
        killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
}

// handleReconnectionState manages the reconnect cycle: wait for internet,
// then periodically attempt tunnel restart until max time is reached.
func (mc *monitoringConfig) handleReconnectionState(state *ProxyState) {
        // First — check if the tunnel recovered on its own (e.g. SSH auto-reconnect).
        // This preserves the original behaviour where the SOCKS check ran in every state.
        if time.Since(mc.lastSocksCheck) >= mc.socksCheckInterval {
                mc.lastSocksCheck = time.Now()
                if checkProxyConnectivityFunc() {
                        mc.isReconnecting = false
                        mc.reconnectAttempts = 0
                        connState.SetStartTime(time.Now())
                        connState.SetActive(true)
                        logTunnelEvent("OK", state.Host, "Tunnel recovered on its own during reconnection")
                        updateTrayStatusOnline(aliasForState(state), remoteForState(state))
                        updateMenuState()
                        pacURL := fmt.Sprintf("http://127.0.0.1:%d/x_proxy.pac", Config.Network.PACHttpPort)
                        setSystemProxy(pacURL)
                        return
                }
        }

        // Check max reconnection time
        if time.Since(mc.reconnectStartTime) > mc.maxReconnectTime {
                logTunnelEvent("ERROR", state.Host,
                        fmt.Sprintf("Max reconnection time exceeded (%d seconds)", Config.Network.MaxReconnectTime))
                handleFatalError(state.Host, state.RemoteHost)
                mc.isFatalError = true
                mc.isReconnecting = false
                connState.SetActive(false)
                updateMenuState()
                return
        }

        // Wait for internet-check delay before starting network probes
        if time.Since(mc.reconnectStartTime) < mc.internetCheckDelay {
                return
        }

        // Check internet connectivity (respecting retry interval)
        if time.Since(mc.lastInternetCheck) >= mc.internetCheckRetry {
                mc.lastInternetCheck = time.Now()
                mc.networkAvailable = checkInternetFunc()

                if !mc.networkAvailable {
                        logTunnelEvent("WARN", state.Host, "No internet connectivity detected")
                        updateTrayStatusNoInternet(aliasForState(state), remoteForState(state),
                                time.Since(mc.reconnectStartTime), mc.maxReconnectTime)
                        return
                }
        } else if !mc.networkAvailable {
                updateTrayStatusNoInternet(aliasForState(state), remoteForState(state),
                        time.Since(mc.reconnectStartTime), mc.maxReconnectTime)
                return
        }

        // Internet is available — attempt reconnection (respecting attempt delay)
        if time.Since(mc.lastReconnectAttempt) < mc.reconnectDelay {
                return
        }
        mc.lastReconnectAttempt = time.Now()
        mc.reconnectAttempts++

        remainingTime := mc.maxReconnectTime - time.Since(mc.reconnectStartTime)
        logTunnelEvent("INFO", state.Host,
                fmt.Sprintf("Reconnection attempt %d (%.0f minutes remaining)",
                        mc.reconnectAttempts, remainingTime.Minutes()))

        updateTrayStatusAttempting(aliasForState(state), remoteForState(state),
                mc.reconnectAttempts, remainingTime)

        if attemptTunnelRestartFunc(state) {
                mc.isReconnecting = false
                mc.reconnectAttempts = 0
                connState.SetStartTime(time.Now())
                connState.SetActive(true)
                logTunnelEvent("OK", state.Host, "Tunnel reestablished after reconnection")
                updateTrayStatusOnline(aliasForState(state), remoteForState(state))
                updateMenuState()
                pacURL := fmt.Sprintf("http://127.0.0.1:%d/x_proxy.pac", Config.Network.PACHttpPort)
                setSystemProxy(pacURL)
        } else {
                logTunnelEvent("ERROR", state.Host,
                        fmt.Sprintf("Reconnection attempt %d failed", mc.reconnectAttempts))
        }
}

// ---------------------------------------------------------------------------
// startMonitoring — thin coordinator (was 280 lines, now ~30)
// ---------------------------------------------------------------------------

// startMonitoring starts a new monitoring loop for the current connection.
func startMonitoring(state *ProxyState) {
        monitoringMutex.Lock()
        if monitoringActive {
                monitoringMutex.Unlock()
                return // already monitoring
        }
        monitoringActive = true
        monitoringMutex.Unlock()

        defer func() {
                monitoringMutex.Lock()
                monitoringActive = false
                monitoringMutex.Unlock()
        }()

        // Drain any leftover stop signals
        select {
        case <-monitoringStopChan:
        default:
        }

        mc := newMonitoringConfig()
        ticker := time.NewTicker(1 * time.Second)
        defer ticker.Stop()

        for {
                select {
                case <-monitoringStopChan:
                        logTunnelEvent("INFO", state.Host, "Monitoring stopped")
                        return

                case <-ticker.C:
                        // Check stop-flag file (written by -stop mode)
                        if _, err := os.Stat(Config.TempFiles.StopFlag); err == nil {
                                logTunnelEvent("INFO", state.Host, "Stop flag detected, monitoring stopped")
                                return
                        }

                        // Periodic original-host return check (failover mode)
                        if mc.checkOriginalHostReturn(state) {
                                return
                        }

                        // State machine
                        switch {
                        case mc.isFatalError:
                                mc.handleFatalErrorState(state)
                        case mc.isReconnecting:
                                mc.handleReconnectionState(state)
                        default:
                                mc.handleNormalState(state)
                        }
                }
        }
}

// ---------------------------------------------------------------------------
// stopMonitoring
// ---------------------------------------------------------------------------

// stopMonitoring stops the current monitoring loop.
func stopMonitoring() {
        monitoringMutex.Lock()
        defer monitoringMutex.Unlock()

        if monitoringActive {
                select {
                case monitoringStopChan <- true:
                default:
                }
                monitoringActive = false
        }
}

// ---------------------------------------------------------------------------
// checkAndRestoreExistingTunnel
// ---------------------------------------------------------------------------

// checkAndRestoreExistingTunnel checks if a tunnel is already running and restores monitoring.
func checkAndRestoreExistingTunnel() {
        if !checkProcessRunning(Config.TempFiles.SSHTunnelPID) {
                tryAutoConnectLastHost()
                return
        }

        // Tunnel exists — wait up to 15s for it to become responsive
        ticker := time.NewTicker(500 * time.Millisecond)
        defer ticker.Stop()
        timeout := time.After(15 * time.Second)

        for {
                select {
                case <-timeout:
                        logTunnelEvent("WARN", "unknown", "Existing tunnel not responding, killing process")
                        killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
                        tryAutoConnectLastHost()
                        return

                case <-ticker.C:
                        if checkProxyConnectivity() {
                                state, err := LoadState()
                                if err == nil {
                                        connState.SetConnected(state.Host)
                                        updateMenuState()
                                        if state.IsChain {
                                                updateTrayStatusOnline("Chain", state.Host)
                                        } else {
                                                updateTrayStatusOnline(state.Host, state.RemoteHost)
                                        }
                                        go startMonitoring(state)
                                        logTunnelEvent("INFO", state.Host, "Restored monitoring for existing tunnel")
                                } else {
                                        logTunnelEvent("WARN", "unknown", "Tunnel active but state missing")
                                }
                                return
                        }
                }
        }
}

// ---------------------------------------------------------------------------
// tryAutoConnectLastHost
// ---------------------------------------------------------------------------

// tryAutoConnectLastHost attempts to auto-connect to the last used host.
func tryAutoConnectLastHost() {
        if !Config.General.AutoConnect {
                return
        }

        lastHost := LoadLastHost()
        if lastHost == "" {
                return
        }

        // Check if lastHost is a chain (contains "|")
        if strings.Contains(lastHost, "|") {
                chainNames := strings.Split(lastHost, "|")
                hosts := parseSSHConfig(Config.Paths.SSHConfig)
                if len(hosts) == 0 {
                        return
                }

                var chain []HostConfig
                for _, name := range chainNames {
                        for _, host := range hosts {
                                if host.Name == name {
                                        chain = append(chain, host)
                                        break
                                }
                        }
                }

                if len(chain) > 0 {
                        go func() {
                                time.Sleep(2 * time.Second)
                                chainDisplay := strings.Join(chainNames, " -> ")
                                establishConnection(ConnectOptions{
                                        Hosts:              chain,
                                        IsChain:            true,
                                        OriginalHost:       chainDisplay,
                                        KillExistingTunnel: true,
                                        EnableSystemProxy:  true,
                                        SaveLastHost:       true,
                                        StartMonitoring:    true,
                                        UpdateTray:         true,
                                        DisplayAlias:       "Chain",
                                        DisplayTooltip:     chainDisplay,
                                })
                        }()
                }
        } else {
                hosts := parseSSHConfig(Config.Paths.SSHConfig)
                if len(hosts) == 0 {
                        return
                }

                for _, host := range hosts {
                        if host.Name == lastHost {
                                firstGroup := hosts[0].Group
                                if host.Group == firstGroup {
                                        go func(h HostConfig) {
                                                time.Sleep(2 * time.Second)
                                                handleHostClick(h)
                                        }(host)
                                }
                                break
                        }
                }
        }
}


