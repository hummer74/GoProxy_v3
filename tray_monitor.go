// tray_monitor.go
package main

import (
        "fmt"
        "image/color"
        "net/http"
        "os"
        "strings"
        "time"

        "github.com/getlantern/systray"
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

// monitoringGeneration is incremented each time a new monitoring session starts.
// It prevents an orphaned goroutine's defer from clearing monitoringActive
// when a newer monitoring session has already taken over.
var monitoringGeneration uint64

// ---------------------------------------------------------------------------
// monitoringConfig — holds all timing and mutable state for a single
// monitoring session, replacing loose local variables.
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
        failState            FailoverState
        reconnectStartTime   time.Time
        lastInternetCheck    time.Time
        lastReconnectAttempt time.Time
        lastSocksCheck       time.Time
        lastOrigHostCheck    time.Time
        lastChainCheck       time.Time
        lastPriorityCheck    time.Time
        networkAvailable     bool
        reconnectAttempts    int

        // Failover / recovery state
        originalChainState *ProxyState // saved when entering Failover (for chain recovery)
        failoverAttempts   int         // how many hosts we've tried in current Failover cycle

        // NoInternet state
        noInternetStart time.Time // when we entered StateNoInternet
        noInternetMax   time.Duration // max wait time (24h)
}

func newMonitoringConfig() *monitoringConfig {
        return &monitoringConfig{
                socksCheckInterval:    time.Duration(Config.Network.SocksCheckInterval) * time.Second,
                internetCheckDelay:    time.Duration(Config.Network.InternetCheckDelay) * time.Second,
                internetCheckRetry:    time.Duration(Config.Network.InternetCheckRetry) * time.Second,
                reconnectDelay:        time.Duration(Config.Network.ReconnectAttemptDelay) * time.Second,
                maxReconnectTime:      time.Duration(Config.Network.MaxReconnectTime) * time.Second,
                origHostCheckInterval: time.Duration(Config.General.OriginalHostCheck) * time.Second,
                failState:             StateNormal,
        }
}

// ---------------------------------------------------------------------------
// Tray-display helpers
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
// Chain host reachability helpers
// ---------------------------------------------------------------------------

// checkChainHostsReachable checks if all hosts in the chain are reachable via SSH.
// Returns (allReachable, list of unavailable host names).
// For reverse hosts (with ProxyJump), checks the ProxyJump target instead.
func checkChainHostsReachable(chainHostNames []string) (bool, []string) {
        if len(chainHostNames) == 0 {
                return true, nil
        }

        hosts := parseSSHConfig(Config.Paths.SSHConfig)
        hostMap := make(map[string]HostConfig)
        for _, h := range hosts {
                hostMap[h.Name] = h
        }

        // Build deduplicated list of effective hosts to check.
        // For reverse hosts, check their ProxyJump target; for direct hosts, check them directly.
        type checkTarget struct {
                name   string
                config HostConfig
        }
        seen := make(map[string]bool)
        var targets []checkTarget

        for _, name := range chainHostNames {
                h, ok := hostMap[name]
                if !ok {
                        // Host not found in SSH config — consider unavailable
                        debugLog("MONITOR", "Chain host %q not found in SSH config", name)
                        return false, []string{name}
                }

                effectiveName := name
                if isReverseHost(h) && h.ProxyJump != "" {
                        effectiveName = h.ProxyJump
                }

                if !seen[effectiveName] {
                        seen[effectiveName] = true
                        if ec, ok := hostMap[effectiveName]; ok {
                                targets = append(targets, checkTarget{name: effectiveName, config: ec})
                        } else {
                                // ProxyJump target not found in config
                                debugLog("MONITOR", "Chain host %q ProxyJump target %q not found", name, effectiveName)
                                return false, []string{effectiveName}
                        }
                }
        }

        // Check all targets in parallel (max 3 concurrent)
        results := checkSSHConnectionBatch(
                func() []HostConfig {
                        var hs []HostConfig
                        for _, t := range targets {
                                hs = append(hs, t.config)
                        }
                        return hs
                }(),
                Config.Paths.WorkDir,
        )

        var unavailable []string
        for _, t := range targets {
                if !results[t.name] {
                        unavailable = append(unavailable, t.name)
                }
        }

        return len(unavailable) == 0, unavailable
}

// attemptChainRecovery tries to re-establish the original chain connection.
// Called from Recovery state when all original chain hosts are verified reachable.
// Does NOT start new monitoring — the caller continues managing the state.
//
// IMPORTANT: This function now performs a pre-flight tunnel test on a temporary
// port BEFORE killing the active (failover) tunnel. If the test fails, the
// failover tunnel stays alive and the function returns false — the caller will
// retry on the next monitoring cycle.
func attemptChainRecovery(originalState *ProxyState) bool {
        if originalState == nil {
                return false
        }

        chainHostNames := originalState.ChainHosts
        isChain := originalState.IsChain

        if !isChain || len(chainHostNames) == 0 {
                // Single host original — handled by checkAndReturnToOriginalHost
                return false
        }

        // Resolve chain hosts from SSH config
        hosts := parseSSHConfig(Config.Paths.SSHConfig)
        hostMap := make(map[string]HostConfig)
        for _, h := range hosts {
                hostMap[h.Name] = h
        }

        var chain []HostConfig
        for _, name := range chainHostNames {
                h, ok := hostMap[name]
                if !ok {
                        debugLog("MONITOR", "Chain recovery: host %q not found in SSH config", name)
                        return false
                }
                chain = append(chain, h)
        }

        if len(chain) != len(chainHostNames) {
                debugLog("MONITOR", "Chain recovery: could not resolve all chain hosts")
                return false
        }

        // Build display string
        var names []string
        for _, h := range chain {
                names = append(names, h.Name)
        }
        chainDisplay := strings.Join(names, " -> ")

        logTunnelEvent("INFO", chainDisplay, "Attempting to restore original chain")

        // ── Pre-flight: test tunnel on temporary port BEFORE killing failover ──
        sshKeyPath := resolveSSHKeyPath(Config.Paths.WorkDir, chain[0].IdentityFile)
        sshKeyPass := loadSSHKeyPassphrase()
        if !ensureSSHAgent(sshKeyPath, sshKeyPass) {
                logTunnelEvent("ERROR", chainDisplay, "Chain recovery: SSH-Agent failed — aborting")
                return false
        }

        testSSHCmd := buildSSHCommand(chain, sshKeyPath)
        testPort := Config.Network.ProxyPort + 1001 // separate from failover test port

        logTunnelEvent("INFO", chainDisplay,
                fmt.Sprintf("Pre-flight chain test on port %d (failover tunnel stays alive)...", testPort))

        // Use longer timeout for chains (base 15s × chain length, up to 45s)
        chainTestTimeout := 15 * time.Duration(len(chain)) * time.Second
        if chainTestTimeout > 45*time.Second {
                chainTestTimeout = 45 * time.Second
        }
        if chainTestTimeout < 15*time.Second {
                chainTestTimeout = 15 * time.Second
        }

        if !testTunnelConnectivity(testSSHCmd, testPort, chainTestTimeout) {
                logTunnelEvent("WARN", chainDisplay,
                        "Pre-flight chain test FAILED — failover tunnel preserved, will retry later")
                return false
        }

        logTunnelEvent("INFO", chainDisplay,
                "Pre-flight chain test PASSED — switching from failover to original chain")

        // ── Safe to switch now: kill failover tunnel and start original chain ──
        // Connect WITHOUT starting new monitoring and WITHOUT stopping current monitoring
        result := establishConnection(ConnectOptions{
                Hosts:              chain,
                IsChain:            true,
                OriginalHost:       originalState.OriginalHost,
                StopMonitoring:     false, // don't stop ourselves
                KillExistingTunnel: true,
                EnableSystemProxy:  true,
                SaveLastHost:       true,
                StartMonitoring:    false, // don't start new monitoring
                UpdateTray:         true,
                DisplayAlias:       "Chain",
                DisplayTooltip:     chainDisplay,
        })

        if result != nil {
                logTunnelEvent("OK", chainDisplay, "Original chain restored successfully")
                return true
        }

        logTunnelEvent("WARN", chainDisplay, "Failed to restore original chain after pre-flight passed, staying in Recovery")
        return false
}

// ---------------------------------------------------------------------------
// State handlers — one method per monitoring state
// ---------------------------------------------------------------------------

// handleNormalState checks SOCKS5 and chain hosts periodically.
// When the tunnel drops or any chain host becomes unreachable, transitions to Failover.
func (mc *monitoringConfig) handleNormalState(state *ProxyState) {
        // ── 1. Periodic chain host monitoring ──
        // Check all hosts in the active chain for reachability.
        // This detects failures even if SSH is in a zombie state (process alive but not working).
        chainCheckInterval := mc.origHostCheckInterval
        if chainCheckInterval < 30*time.Second {
                chainCheckInterval = 30 * time.Second
        }

        if time.Since(mc.lastChainCheck) >= chainCheckInterval {
                mc.lastChainCheck = time.Now()

                // Determine which hosts to check
                var chainHostNames []string
                if state.IsChain && len(state.ChainHosts) > 0 {
                        chainHostNames = state.ChainHosts
                } else {
                        // Single host — check just that host
                        chainHostNames = []string{state.Host}
                }

                allOk, failedHosts := checkChainHostsReachable(chainHostNames)
                if !allOk {
                        logTunnelEvent("ERROR", state.Host,
                                fmt.Sprintf("Chain host(s) unavailable: %v — entering Failover", failedHosts))
                        mc.enterFailoverState(state)
                        return
                }
                debugLog("MONITOR", "Chain hosts check: all OK (%d hosts)", len(chainHostNames))
        }

        // ── 2. SOCKS5 connectivity check ──
        if time.Since(mc.lastSocksCheck) < mc.socksCheckInterval {
                return
        }
        mc.lastSocksCheck = time.Now()

        if checkProxyConnectivityFunc() {
                // Tunnel is online

                // Check priority host availability periodically
                if hasPriorityHost && connState.GetHost() != priorityHost && time.Since(mc.lastPriorityCheck) >= mc.origHostCheckInterval {
                        mc.lastPriorityCheck = time.Now()
                        hosts := parseSSHConfig(Config.Paths.SSHConfig)
                        var priorityHostConfig *HostConfig
                        for _, h := range hosts {
                                if h.Name == priorityHost {
                                        priorityHostConfig = &h
                                        break
                                }
                        }
                        if priorityHostConfig != nil {
                                available, responseTime := checkSSHConnectionWithTime(*priorityHostConfig, Config.Paths.WorkDir)
                                if available {
                                        logTunnelEvent("INFO", connState.GetHost(), fmt.Sprintf("Priority host %s is available (%.2fs), switching back", priorityHost, responseTime.Seconds()))
                                        result := establishConnection(ConnectOptions{
                                                Hosts:              []HostConfig{*priorityHostConfig},
                                                OriginalHost:       priorityHost,
                                                StopMonitoring:     true,
                                                KillExistingTunnel: true,
                                                EnableSystemProxy:  true,
                                                SaveLastHost:       false,
                                                StartMonitoring:    true,
                                                UpdateTray:         true,
                                        })
                                        if result == nil {
                                                logTunnelEvent("ERROR", priorityHost, "Failed to switch to priority host")
                                        } else {
                                                logTunnelEvent("OK", priorityHost, "Switched to priority host")
                                        }
                                        return
                                }
                        }
                }

                return
        }

        // ── 3. Tunnel went offline ──
        logTunnelEvent("ERROR", state.Host, "Tunnel lost connection (SOCKS5 not responding)")
        mc.enterFailoverState(state)
}

// enterFailoverState saves the original chain state and transitions to Failover.
func (mc *monitoringConfig) enterFailoverState(state *ProxyState) {
        // Save original state for recovery
        stateCopy := *state
        mc.originalChainState = &stateCopy
        mc.failoverAttempts = 0

        // Transition to Failover
        mc.failState = StateFailover
        connState.SetFailState(StateFailover)
        connState.SetActive(false)

        debugLog("MONITOR", "Entering Failover state from Normal (original host: %s)", state.Host)
        updateTrayStatusReconnecting(aliasForState(state), remoteForState(state))
        updateMenuState()
}

// handleFailoverState finds the fastest available host and connects to it.
// On success → transitions to Recovery.
// If all hosts fail → transitions to ErrorHalt.
func (mc *monitoringConfig) handleFailoverState(state *ProxyState) bool {
        debugLog("MONITOR", "handleFailoverState: attempt %d", mc.failoverAttempts+1)

        // ── 1. Gather available hosts (all groups, exclude current and reverse) ──
        hosts := parseSSHConfig(Config.Paths.SSHConfig)
        if len(hosts) == 0 {
                mc.enterErrorHalt(state, "No hosts found in SSH config")
                return false
        }

        // Determine which host to exclude (the one that just failed)
        excludeHost := ""
        if mc.originalChainState != nil {
                excludeHost = mc.originalChainState.Host
        }

        var availableHosts []HostConfig
        for _, h := range hosts {
                // Exclude the host/chain that just failed
                if h.Name == excludeHost {
                        continue
                }
                // Exclude reverse hosts (they need a jumper that might be down)
                if isReverseHost(h) {
                        continue
                }
                // Exclude REVERSE JUMPER hosts (used only for building reverse chains)
                if isReverseJumperHost(h) {
                        continue
                }
                availableHosts = append(availableHosts, h)
        }

        if len(availableHosts) == 0 {
                logTunnelEvent("ERROR", state.Host, "No alternative hosts available for failover")
                mc.enterErrorHalt(state, "No alternative hosts available")
                return false
        }

        // ── 2. Find and connect to the fastest available host ──
        logTunnelEvent("INFO", state.Host, fmt.Sprintf("Looking for fastest among %d available hosts...", len(availableHosts)))
        fastestHost, responseTime := findFastestAvailableHost(availableHosts, Config.Paths.WorkDir)

        if fastestHost == nil {
                logTunnelEvent("WARN", state.Host, "No reachable hosts found for failover")
                mc.failoverAttempts++
                if mc.failoverAttempts >= 3 {
                        mc.enterErrorHalt(state, "All failover attempts exhausted")
                        return false
                }
                return false
        }

        responseTimeSec := responseTime.Seconds()
        logTunnelEvent("INFO", state.Host,
                fmt.Sprintf("Fastest host: %s (%.2fs), connecting...", fastestHost.Name, responseTimeSec))

        updateTrayStatusReconnecting(fastestHost.Name, fastestHost.HostName)

        // ── 3. Pre-flight tunnel test on a temporary port ──
        // Verify that a full SOCKS5 tunnel can actually be established BEFORE
        // killing the existing (zombie) tunnel process.
        sshKeyPath := resolveSSHKeyPath(Config.Paths.WorkDir, fastestHost.IdentityFile)
        sshKeyPass := loadSSHKeyPassphrase()
        if !ensureSSHAgent(sshKeyPath, sshKeyPass) {
                logTunnelEvent("ERROR", fastestHost.Name, "SSH-Agent failed — skipping host")
                mc.failoverAttempts++
                if mc.failoverAttempts >= 5 {
                        mc.enterErrorHalt(state, "Exhausted failover attempts (5)")
                        return false
                }
                return false
        }

        testSSHCmd := buildSSHCommand([]HostConfig{*fastestHost}, sshKeyPath)
        testPort := Config.Network.ProxyPort + 1000 // use a port far from the production one

        logTunnelEvent("INFO", fastestHost.Name,
                fmt.Sprintf("Pre-flight tunnel test on port %d...", testPort))

        if !testTunnelConnectivity(testSSHCmd, testPort, 20*time.Second) {
                logTunnelEvent("WARN", fastestHost.Name,
                        "Pre-flight tunnel test FAILED — skipping, will try next host")
                mc.failoverAttempts++
                if mc.failoverAttempts >= 5 {
                        mc.enterErrorHalt(state, "Exhausted failover attempts (5)")
                        return false
                }
                return false
        }

        logTunnelEvent("INFO", fastestHost.Name,
                "Pre-flight test PASSED — safe to switch")

        // ── 4. Connect to failover host (DON'T stop/start monitoring) ──
        originalHost := state.Host
        if mc.originalChainState != nil {
                originalHost = mc.originalChainState.OriginalHost
        }

        result := establishConnection(ConnectOptions{
                Hosts:              []HostConfig{*fastestHost},
                OriginalHost:       originalHost,
                IsFailoverActive:   true,
                FailoverStart:      time.Now().Format(time.RFC3339),
                StopMonitoring:     false, // don't stop ourselves
                KillExistingTunnel: true,
                EnableSystemProxy:  true,
                SaveLastHost:       false,
                StartMonitoring:    false, // don't start new monitoring
                UpdateTray:         true,
                DisplayAlias:       fastestHost.Name,
                DisplayTooltip:     fastestHost.HostName,
        })

        if result != nil {
                logTunnelEvent("OK", state.Host,
                        fmt.Sprintf("Failover successful: switched to %s", fastestHost.Name))

                // Transition to Recovery — we're now on a failover host,
                // periodically checking if the original chain can be restored.
                mc.failState = StateRecovery
                connState.SetFailState(StateRecovery)
                mc.lastOrigHostCheck = time.Time{} // reset to check immediately

                debugLog("MONITOR", "Failover → Recovery (failover host: %s)", fastestHost.Name)
                return false
        }

        // Connection to this host failed, try again next tick
        logTunnelEvent("ERROR", fastestHost.Name, "Failed to connect to failover host")
        mc.failoverAttempts++
        if mc.failoverAttempts >= 5 {
                mc.enterErrorHalt(state, "Exhausted failover attempts (5)")
                return false
        }

        return false
}

// handleRecoveryState monitors the failover host and periodically checks
// if all original chain hosts are available for restoration.
// When all original hosts are verified → re-establishes original chain → Normal.
// If failover host drops → back to Failover.
func (mc *monitoringConfig) handleRecoveryState(state *ProxyState) bool {
        // ── 1. Check failover host (SOCKS5) ──
        if time.Since(mc.lastSocksCheck) >= mc.socksCheckInterval {
                mc.lastSocksCheck = time.Now()

                if !checkProxyConnectivityFunc() {
                        // Failover host itself dropped — return to Failover state
                        logTunnelEvent("ERROR", state.Host, "Failover host lost connection, returning to Failover state")
                        mc.failState = StateFailover
                        connState.SetFailState(StateFailover)
                        connState.SetActive(false)
                        mc.failoverAttempts = 0
                        updateTrayStatusReconnecting(aliasForState(state), remoteForState(state))
                        updateMenuState()
                        return false
                }
        }

        // ── 2. Periodically check if original chain can be restored ──
        if mc.origHostCheckInterval == 0 || time.Since(mc.lastOrigHostCheck) < mc.origHostCheckInterval {
                return false
        }
        mc.lastOrigHostCheck = time.Now()

        if mc.originalChainState == nil {
                // No original state saved — nothing to recover
                return false
        }

        origState := mc.originalChainState

        if origState.IsChain && len(origState.ChainHosts) > 0 {
                // ── Chain recovery: check ALL hosts in the original chain ──
                allOk, failedHosts := checkChainHostsReachable(origState.ChainHosts)

                if allOk {
                        logTunnelEvent("INFO", state.Host,
                                fmt.Sprintf("All original chain hosts are reachable, attempting chain restoration"))
                        if attemptChainRecovery(origState) {
                                // Chain restored — transition to Normal
                                mc.failState = StateNormal
                                connState.SetFailState(StateNormal)
                                connState.SetActive(true)
                                mc.originalChainState = nil // clear saved state

                                logTunnelEvent("OK", origState.Host, "Original chain restored — returning to Normal state")
                                updateMenuState()
                                return false
                        }
                        // Chain restoration failed — stay in Recovery, will retry next cycle
                } else {
                        debugLog("MONITOR", "Recovery: chain hosts still unavailable: %v", failedHosts)
                }
        } else {
                // ── Single host recovery: check original host ──
                origName := origState.OriginalHost
                if origName == "" {
                        origName = origState.Host
                }

                hosts := parseSSHConfig(Config.Paths.SSHConfig)
                var origConfig *HostConfig
                for _, h := range hosts {
                        if h.Name == origName {
                                origConfig = &h
                                break
                        }
                }

                if origConfig == nil {
                        debugLog("MONITOR", "Recovery: original host %q not found in SSH config", origName)
                        return false
                }

                available, responseTime := checkSSHConnectionWithTime(*origConfig, Config.Paths.WorkDir)
                if available {
                        logTunnelEvent("INFO", state.Host,
                                fmt.Sprintf("Original host %s is available (%.2fs), restoring...", origName, responseTime.Seconds()))

                        // ── Pre-flight: test tunnel on temporary port BEFORE killing failover ──
                        origSSHKeyPath := resolveSSHKeyPath(Config.Paths.WorkDir, origConfig.IdentityFile)
                        origSSHKeyPass := loadSSHKeyPassphrase()
                        if !ensureSSHAgent(origSSHKeyPath, origSSHKeyPass) {
                                logTunnelEvent("ERROR", origName, "Recovery: SSH-Agent failed — aborting restore")
                                return false
                        }

                        testSSHCmd := buildSSHCommand([]HostConfig{*origConfig}, origSSHKeyPath)
                        testPort := Config.Network.ProxyPort + 1002 // separate from other test ports

                        logTunnelEvent("INFO", origName,
                                fmt.Sprintf("Pre-flight restore test on port %d (failover tunnel stays alive)...", testPort))

                        if !testTunnelConnectivity(testSSHCmd, testPort, 20*time.Second) {
                                logTunnelEvent("WARN", origName,
                                        "Pre-flight restore test FAILED — failover tunnel preserved, will retry later")
                                return false
                        }

                        logTunnelEvent("INFO", origName,
                                "Pre-flight restore test PASSED — switching from failover to original")

                        // Safe to switch now: kill failover tunnel and restore original
                        result := establishConnection(ConnectOptions{
                                Hosts:              []HostConfig{*origConfig},
                                OriginalHost:       origName,
                                StopMonitoring:     false, // don't stop ourselves
                                KillExistingTunnel: true,
                                EnableSystemProxy:  true,
                                SaveLastHost:       true,
                                StartMonitoring:    false, // don't start new monitoring
                                UpdateTray:         true,
                                DisplayAlias:       origConfig.Name,
                                DisplayTooltip:     origConfig.HostName,
                        })

                        if result != nil {
                                // Original host restored — transition to Normal
                                mc.failState = StateNormal
                                connState.SetFailState(StateNormal)
                                connState.SetActive(true)
                                mc.originalChainState = nil // clear saved state

                                logTunnelEvent("OK", origName, "Returned to original host — Normal state")
                                updateMenuState()
                        } else {
                                logTunnelEvent("WARN", origName, "Failed to return to original host after pre-flight passed, staying in Recovery")
                        }
                } else {
                        debugLog("MONITOR", "Recovery: original host %s still unavailable", origName)
                }
        }

        return false
}

// handleFatalErrorState periodically pings SOCKS5; if the tunnel recovers on
// its own the state resets to normal.
func (mc *monitoringConfig) handleFatalErrorState(state *ProxyState) {
        if time.Since(mc.lastSocksCheck) < mc.socksCheckInterval {
                return
        }
        mc.lastSocksCheck = time.Now()

        if checkProxyConnectivityFunc() {
                // Tunnel recovered — reset to Normal
                mc.failState = StateNormal
                connState.SetFailState(StateNormal)
                connState.SetStartTime(time.Now())
                connState.SetActive(true)
                logTunnelEvent("OK", state.Host, "Tunnel recovered from Error/Halt state")
                updateTrayStatusOnline(aliasForState(state), remoteForState(state))
                updateMenuState()
        }
}

// handleNoInternetState waits for internet connectivity to return.
// Checks every 1 minute, up to 24 hours.
// When internet returns → attempts auto-connect like program startup.
// When 24h expires → transitions to ErrorHalt.
func (mc *monitoringConfig) handleNoInternetState(state *ProxyState) {
        elapsed := time.Since(mc.noInternetStart)

        // Check timeout (24 hours)
        if elapsed >= mc.noInternetMax {
                logTunnelEvent("ERROR", state.Host,
                        fmt.Sprintf("NoInternet: 24h wait expired — entering Error/Halt"))
                mc.failState = StateErrorHalt
                connState.SetFailState(StateErrorHalt)

                systray.SetTitle("FATAL ERROR")
                systray.SetTooltip(fmt.Sprintf("%s: ERROR/HALT\nNo internet for 24h", state.Host))
                updateMenuState()
                return
        }

        // Check internet every 1 minute
        if elapsed < 1*time.Minute {
                // First check happens after 1 minute
                return
        }

        // Throttle to 1-minute intervals using lastSocksCheck
        if time.Since(mc.lastSocksCheck) < 1*time.Minute {
                return
        }
        mc.lastSocksCheck = time.Now()

        logTunnelEvent("INFO", state.Host,
                fmt.Sprintf("NoInternet: checking internet (%.0f min elapsed)...", elapsed.Minutes()))

        alias := aliasForState(state)
        remote := remoteForState(state)
        updateTrayStatusNoInternet(alias, remote, elapsed, mc.noInternetMax)

        if !checkInternetDirect() {
                debugLog("MONITOR", "NoInternet: still no connectivity")
                return
        }

        // Internet is back!
        logTunnelEvent("OK", state.Host, "NoInternet: internet connectivity restored!")

        // Attempt auto-connect like program startup
        killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")

        // tryAutoConnectLastHost runs in its own goroutine and starts monitoring
        // We need to stop the current monitoring session so tryAutoConnectLastHost
        // can start fresh. But we can't stop ourselves — so we transition to
        // Normal state and let tryAutoConnectLastHost handle everything.
        mc.failState = StateNormal
        connState.SetFailState(StateNormal)

        systray.SetTitle("Internet restored — reconnecting...")
        updateMenuState()

        logTunnelEvent("INFO", state.Host, "NoInternet: launching tryAutoConnectLastHost")
        go tryAutoConnectLastHost()
}

// enterErrorHalt transitions to Error/Halt or NoInternet depending on connectivity.
// If internet is available → true ErrorHalt (all hosts really are down).
// If internet is NOT available → StateNoInternet (wait for network, then auto-restore).
func (mc *monitoringConfig) enterErrorHalt(state *ProxyState, reason string) {
        logTunnelEvent("ERROR", state.Host, fmt.Sprintf("All hosts failed: %s — checking internet...", reason))

        // Immediately disable proxy and kill tunnel — no point keeping them alive
        disableSystemProxy()
        killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
        connState.SetActive(false)

        // Check if internet is available (direct, without proxy)
        if checkInternetDirect() {
                // Internet works but all hosts are down → true ErrorHalt
                logTunnelEvent("ERROR", state.Host, "Internet is available but all hosts unreachable — entering Error/Halt")

                mc.failState = StateErrorHalt
                connState.SetFailState(StateErrorHalt)

                iconData := loadIconData(color.RGBA{255, 0, 0, 255})
                if iconData != nil {
                        systray.SetIcon(iconData)
                }
                systray.SetTitle("FATAL ERROR")
                systray.SetTooltip(fmt.Sprintf("%s: ERROR/HALT\n%s\nManual intervention required", state.Host, state.RemoteHost))
                updateMenuState()
                return
        }

        // No internet → enter NoInternet state
        logTunnelEvent("WARN", state.Host, "No internet connectivity — entering NoInternet state (24h wait)")

        mc.failState = StateNoInternet
        mc.noInternetStart = time.Now()
        mc.noInternetMax = 24 * time.Hour
        connState.SetFailState(StateNoInternet)

        alias := aliasForState(state)
        remote := remoteForState(state)
        updateTrayStatusNoInternet(alias, remote, 0, mc.noInternetMax)
        updateMenuState()
}

// checkInternetDirect checks internet connectivity without using the proxy.
// Performs a direct HTTP request to known-good endpoints.
func checkInternetDirect() bool {
        testURLs := []string{
                "https://clients1.google.com/generate_204",
                "http://connectivitycheck.gstatic.com/generate_204",
        }

        client := &http.Client{Timeout: 5 * time.Second}
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

// ---------------------------------------------------------------------------
// startMonitoring — coordinator with 4-state machine
// ---------------------------------------------------------------------------

// startMonitoring starts a new monitoring loop for the current connection.
func startMonitoring(state *ProxyState) {
        debugLog("MONITOR", "Starting monitoring for: %s", state.Host)

        monitoringMutex.Lock()
        if monitoringActive {
                monitoringMutex.Unlock()
                return // already monitoring
        }
        monitoringActive = true
        monitoringGeneration++
        myGeneration := monitoringGeneration
        monitoringMutex.Unlock()

        defer func() {
                monitoringMutex.Lock()
                if monitoringGeneration == myGeneration {
                        monitoringActive = false
                }
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

                        // 5-state machine
                        switch mc.failState {
                        case StateNormal:
                                mc.handleNormalState(state)

                        case StateFailover:
                                mc.handleFailoverState(state)
                                // After failover connection, reload state from disk
                                if s, err := LoadState(); err == nil {
                                        state = s
                                }

                        case StateRecovery:
                                mc.handleRecoveryState(state)
                                // After chain recovery, reload state from disk
                                if s, err := LoadState(); err == nil {
                                        state = s
                                }

                        case StateNoInternet:
                                mc.handleNoInternetState(state)

                        case StateErrorHalt:
                                mc.handleFatalErrorState(state)
                        }
                }
        }
}

// ---------------------------------------------------------------------------
// stopMonitoring
// ---------------------------------------------------------------------------

// stopMonitoring stops the current monitoring loop.
func stopMonitoring() {
        debugLog("MONITOR", "Stopping monitoring")

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
        debugLog("MONITOR", "Checking for existing tunnel...")

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

// tryAutoConnectLastHost attempts to auto-connect to the priority host from x_lasthost.cfg
func tryAutoConnectLastHost() {
        if !Config.General.AutoConnect {
                return
        }

        priorityHostName := LoadPriorityHost()
        if priorityHostName == "" {
                return
        }

        debugLog("MONITOR", "Auto-connect: priorityHost=%s", priorityHostName)

        // Check if priorityHost is a chain (contains "|")
        if strings.Contains(priorityHostName, "|") {
                chainNames := strings.Split(priorityHostName, "|")
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
                        // Check if first host in chain is available
                        available, _ := checkSSHConnectionWithTime(chain[0], Config.Paths.WorkDir)
                        if !available {
                                debugLog("MONITOR", "Priority chain first host %s unavailable, looking for fallback", chain[0].Name)
                                // Find fastest available host
                                var availableHosts []HostConfig
                                for _, h := range hosts {
                                        if h.Group == hosts[0].Group && !strings.Contains(h.Name, "Rev-") && h.ProxyJump == "" {
                                                availableHosts = append(availableHosts, h)
                                        }
                                }
                                // Exclude REVERSE JUMPER hosts from fallback
                                var filteredHosts []HostConfig
                                for _, h := range availableHosts {
                                        if !isReverseJumperHost(h) {
                                                filteredHosts = append(filteredHosts, h)
                                        }
                                }
                                fastestHost, _ := findFastestAvailableHost(filteredHosts, Config.Paths.WorkDir)
                                if fastestHost != nil {
                                        debugLog("MONITOR", "Fallback: switching to %s", fastestHost.Name)
                                        safeGo(func() {
                                                time.Sleep(2 * time.Second)
                                                establishConnection(ConnectOptions{
                                                        Hosts:              []HostConfig{*fastestHost},
                                                        OriginalHost:       priorityHostName,
                                                        IsFailoverActive:   true,
                                                        FailoverStart:      time.Now().Format(time.RFC3339),
                                                        KillExistingTunnel: true,
                                                        EnableSystemProxy:  true,
                                                        SaveLastHost:       false,
                                                        StartMonitoring:    true,
                                                        UpdateTray:         true,
                                                })
                                        })
                                } else {
                                        debugLog("MONITOR", "No available hosts for fallback")
                                }
                        } else {
                                // Priority chain available, connect to it
                                safeGo(func() {
                                        time.Sleep(2 * time.Second)
                                        chainDisplay := strings.Join(chainNames, " -> ")
                                        establishConnection(ConnectOptions{
                                                Hosts:              chain,
                                                IsChain:            true,
                                                OriginalHost:       priorityHostName,
                                                KillExistingTunnel: true,
                                                EnableSystemProxy:  true,
                                                SaveLastHost:       false,
                                                StartMonitoring:    true,
                                                UpdateTray:         true,
                                                DisplayAlias:       "Chain",
                                                DisplayTooltip:     chainDisplay,
                                        })
                                })
                        }
                }
        } else {
                hosts := parseSSHConfig(Config.Paths.SSHConfig)
                if len(hosts) == 0 {
                        return
                }

                var priorityHostConfig *HostConfig
                for _, host := range hosts {
                        if host.Name == priorityHostName {
                                priorityHostConfig = &host
                                break
                        }
                }
                if priorityHostConfig == nil {
                        return
                }

                available, _ := checkSSHConnectionWithTime(*priorityHostConfig, Config.Paths.WorkDir)
                if !available {
                        debugLog("MONITOR", "Priority host %s unavailable, looking for fallback", priorityHostName)
                        // Find fastest available host
                        var availableHosts []HostConfig
                        for _, h := range hosts {
                                if h.Group == hosts[0].Group && h.Name != priorityHostName && !strings.Contains(h.Name, "Rev-") && h.ProxyJump == "" {
                                        availableHosts = append(availableHosts, h)
                                }
                        }
                        // Exclude REVERSE JUMPER hosts from fallback
                        var filteredHosts []HostConfig
                        for _, h := range availableHosts {
                                if !isReverseJumperHost(h) {
                                        filteredHosts = append(filteredHosts, h)
                                }
                        }
                        fastestHost, _ := findFastestAvailableHost(filteredHosts, Config.Paths.WorkDir)
                        if fastestHost != nil {
                                debugLog("MONITOR", "Fallback: switching to %s", fastestHost.Name)
                                safeGo(func() {
                                        time.Sleep(2 * time.Second)
                                        establishConnection(ConnectOptions{
                                                Hosts:              []HostConfig{*fastestHost},
                                                OriginalHost:       priorityHostName,
                                                IsFailoverActive:   true,
                                                FailoverStart:      time.Now().Format(time.RFC3339),
                                                KillExistingTunnel: true,
                                                EnableSystemProxy:  true,
                                                SaveLastHost:       false,
                                                StartMonitoring:    true,
                                                UpdateTray:         true,
                                        })
                                })
                        } else {
                                debugLog("MONITOR", "No available hosts for fallback")
                        }
                } else {
                        // Priority host available, connect to it
                        safeGo(func() {
                                time.Sleep(2 * time.Second)
                                establishConnection(ConnectOptions{
                                        Hosts:              []HostConfig{*priorityHostConfig},
                                        OriginalHost:       priorityHostName,
                                        KillExistingTunnel: true,
                                        EnableSystemProxy:  true,
                                        SaveLastHost:       false,
                                        StartMonitoring:    true,
                                        UpdateTray:         true,
                                })
                        })
                }
        }
}
