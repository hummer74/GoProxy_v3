// tray_monitor.go
package main

import (
	"fmt"
	"image/color"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
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

// Monitoring activity is guarded by monitoringActive + monitoringMutex
// (see tray_main.go); session identity comes solely from connMgr.generation().

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

	// SOCKS debounce: consecutive failures (failover at SocksFailThreshold).
	socksFails int

	// Failover loop start (give up after MaxFailoverDuration).
	failoverStart time.Time

	// ErrorHalt retry: the halt may resolve on its own (transient outage).
	errorHaltRetry bool
	errorHaltStart time.Time

	// Uplink probe state (end-to-end traffic check through the tunnel).
	lastUplinkCheck time.Time
	uplinkFails     int

	// Last monitor tick (sleep/wake gap detection).
	lastTick time.Time

	// Failover / recovery state
	originalChainState *ProxyState // saved when entering Failover (for chain recovery)
	failoverAttempts   int         // how many hosts we've tried in current Failover cycle

	// NoInternet state
	noInternetStart time.Time     // when we entered StateNoInternet
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
		lastPriorityCheck:     time.Now(), // don't fire priority check immediately on first tick
		failState:             StateNormal,
	}
}

// failoverBackoff — экспоненциальный бэкофф для бесконечного failover (по анкете):
// 5s, 10s, 20s ... cap 60s. Только для логов/метрик, тик монитора 1с остаётся.
func failoverBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := 5 * time.Second << (attempt - 1)
	if d > 60*time.Second || d <= 0 {
		return 60 * time.Second
	}
	return d
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
	results, _ := checkSSHConnectionBatch(
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

// ---------------------------------------------------------------------------
// Priority/original canonical helpers (P1: chains)
// ---------------------------------------------------------------------------
// The user-selected target is stored pipe-separated ("name" or "a|b|c",
// see SaveLastHost), while a live chain displays arrow-separated
// ("a -> b"). Direct string comparison between the two can never match,
// so all priority/original checks go through these helpers.

// splitPriorityNames splits a priority/lasthost value into member names.
func splitPriorityNames(priority string) []string {
	if strings.Contains(priority, "|") {
		return strings.Split(priority, "|")
	}
	return []string{priority}
}

// priorityMatchesState reports whether the live connection already IS the
// priority target: single host by name, chain by ordered member set.
func priorityMatchesState(state *ProxyState, priority string) bool {
	if state == nil || priority == "" {
		return false
	}
	if !strings.Contains(priority, "|") {
		return !state.IsChain && state.Host == priority
	}
	want := strings.Split(priority, "|")
	if !state.IsChain || len(state.ChainHosts) != len(want) {
		return false
	}
	for i := range want {
		if state.ChainHosts[i] != want[i] {
			return false
		}
	}
	return true
}

// buildFailoverExcludeSet returns the names that must never be failover
// candidates: every member of the failed original (single name or full
// chain set). Comparing against the display string ("a -> b") never
// matches a member name, so chains are expanded explicitly.
func buildFailoverExcludeSet(orig *ProxyState) map[string]bool {
	exclude := make(map[string]bool)
	if orig == nil {
		return exclude
	}
	if len(orig.ChainHosts) > 0 {
		for _, n := range orig.ChainHosts {
			exclude[n] = true
		}
		return exclude
	}
	if orig.Host != "" && !strings.Contains(orig.Host, " -> ") {
		exclude[orig.Host] = true
		return exclude
	}
	if orig.OriginalHost != "" && !strings.Contains(orig.OriginalHost, " -> ") {
		exclude[orig.OriginalHost] = true
	}
	return exclude
}

// restoreOriginalFromDisk rebuilds the pre-failover original from a persisted
// failover ProxyState (x_proxy_state.json). Fresh monitoring sessions start
// with an empty originalChainState; without this, a restart while sitting on
// a failover host permanently forgets where to return (the original is then
// overwritten by the failover host on the next drop). Returns nil when there
// is nothing to restore.
func restoreOriginalFromDisk(failover *ProxyState) *ProxyState {
	if failover == nil || !failover.IsFailoverActive {
		return nil
	}
	orig := failover.OriginalHost
	if orig == "" {
		return nil
	}
	if strings.Contains(orig, " -> ") {
		names := strings.Split(orig, " -> ")
		if len(names) == 0 {
			return nil
		}
		return &ProxyState{
			IsChain:      true,
			Host:         orig,
			OriginalHost: orig,
			ChainHosts:   names,
		}
	}
	return &ProxyState{
		IsChain:      false,
		Host:         orig,
		OriginalHost: orig,
	}
}

// attemptChainRecovery tries to re-establish the original chain connection.
// Called from Recovery state when all original chain hosts are verified reachable.
// Does NOT start new monitoring — the caller continues managing the state.
// Returns (true, gen) on success so the caller can adopt the new session;
// gen is 0 when no new session was created.
//
// IMPORTANT: This function now performs a pre-flight tunnel test on a temporary
// port BEFORE killing the active (failover) tunnel. If the test fails, the
// failover tunnel stays alive and the function returns false — the caller will
// retry on the next monitoring cycle.
func attemptChainRecovery(originalState *ProxyState) (bool, uint64) {
	if originalState == nil {
		return false, 0
	}

	chainHostNames := originalState.ChainHosts
	isChain := originalState.IsChain

	if !isChain || len(chainHostNames) == 0 {
		// Single host original — handled by checkAndReturnToOriginalHost
		return false, 0
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
			return false, 0
		}
		chain = append(chain, h)
	}

	if len(chain) != len(chainHostNames) {
		debugLog("MONITOR", "Chain recovery: could not resolve all chain hosts")
		return false, 0
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
		return false, 0
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

	if ok, freason := probeTunnelWithReason(testSSHCmd, testPort, chainTestTimeout); !ok {
		if freason == "auth" {
			logTunnelEvent("ERROR", chainDisplay,
				"Pre-flight chain test FAILED: SSH key rejected — fix the key, failover tunnel stays up")
		} else {
			logTunnelEvent("WARN", chainDisplay,
				"Pre-flight chain test FAILED — failover tunnel preserved, will retry later")
		}
		return false, 0
	}

	logTunnelEvent("INFO", chainDisplay,
		"Pre-flight chain test PASSED — switching from failover to original chain")

	// ── Safe to switch now: kill failover tunnel and start original chain ──
	// Connect WITHOUT starting new monitoring and WITHOUT stopping current monitoring
	result, gen := establishConnection(ConnectOptions{
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
		return true, gen
	}

	logTunnelEvent("WARN", chainDisplay, "Failed to restore original chain after pre-flight passed, staying in Recovery")
	return false, gen
}

// ---------------------------------------------------------------------------
// State handlers — one method per monitoring state
// ---------------------------------------------------------------------------

// handleNormalState checks SOCKS5 (ground truth) and chain hosts (advisory).
// Failover triggers ONLY on SOCKS loss. Slow direct SSH checks (2-7s vs 5s
// timeout, см. 04-FN.206) больше не роняют рабочий туннель — только WARN.
func (mc *monitoringConfig) handleNormalState(state *ProxyState) {
	// ── 1. SOCKS5 connectivity check (ground truth) ──
	if time.Since(mc.lastSocksCheck) < mc.socksCheckInterval {
		return
	}
	mc.lastSocksCheck = time.Now()

	if !checkProxyConnectivityFunc() {
		// ── Tunnel may be offline — debounce consecutive failures ──
		if mc.recordSocks(false) {
			logTunnelEvent("ERROR", state.Host,
				fmt.Sprintf("Tunnel lost connection (SOCKS5 not responding %dx) — failing over", SocksFailThreshold))
			mc.enterFailoverState(state)
		} else {
			logTunnelEvent("WARN", state.Host,
				fmt.Sprintf("SOCKS5 check failed (%d/%d) — waiting, tunnel may flap", mc.socksFails, SocksFailThreshold))
		}
		return
	}
	mc.recordSocks(true)

	// ── 1b. Uplink check: SOCKS handshake succeeds even with a dead uplink
	// (local ssh.exe keeps listening) — prove real traffic flows through.
	// N consecutive end-to-end failures = tunnel loss, same as SOCKS loss.
	if time.Since(mc.lastUplinkCheck) >= UplinkCheckInterval {
		mc.lastUplinkCheck = time.Now()
		if checkUplinkFunc() {
			mc.recordUplink(true)
		} else if mc.recordUplink(false) {
			logTunnelEvent("ERROR", state.Host,
				fmt.Sprintf("No traffic through tunnel (%dx) — uplink dead, failing over", UplinkFailThreshold))
			mc.enterFailoverState(state)
			return
		} else {
			logTunnelEvent("WARN", state.Host,
				fmt.Sprintf("Uplink probe failed (%d/%d) — waiting, tunnel may flap", mc.uplinkFails, UplinkFailThreshold))
		}
	}

	// ── 2. Periodic chain host monitoring (advisory only) ──
	// Detects degradation early, but NEVER fails over a working tunnel:
	// direct SSH checks flap on slow hosts (false "unavailable").
	chainCheckInterval := mc.origHostCheckInterval
	if chainCheckInterval < 30*time.Second {
		chainCheckInterval = 30 * time.Second
	}

	if time.Since(mc.lastChainCheck) >= chainCheckInterval {
		mc.lastChainCheck = time.Now()

		var chainHostNames []string
		if state.IsChain && len(state.ChainHosts) > 0 {
			chainHostNames = state.ChainHosts
		} else {
			chainHostNames = []string{state.Host}
		}

		allOk, failedHosts := checkChainHostsReachable(chainHostNames)
		if !allOk {
			logTunnelEvent("WARN", state.Host,
				fmt.Sprintf("Chain host(s) slow/unreachable: %v — tunnel alive, staying Normal", failedHosts))
		} else {
			debugLog("MONITOR", "Chain hosts check: all OK (%d hosts)", len(chainHostNames))
		}
	}

	// Tunnel is online (verified above) — priority host check only when
	// this connection is a failover (not a user-initiated manual switch)
	// and the user allowed automatic return (ReturnToOriginalHost).
	if Config.General.ReturnToOriginalHost && hasPriorityHost && state.IsFailoverActive && !priorityMatchesState(state, priorityHost) && time.Since(mc.lastPriorityCheck) >= mc.origHostCheckInterval {
		mc.lastPriorityCheck = time.Now()
		// Chain priority ("a|b|c"): hand over to the Recovery machinery —
		// it owns chain reachability + pre-flight + restore. Single-name
		// priority is handled inline below.
		if strings.Contains(priorityHost, "|") {
			names := splitPriorityNames(priorityHost)
			mc.originalChainState = &ProxyState{
				IsChain:      true,
				Host:         strings.Join(names, " -> "),
				OriginalHost: state.OriginalHost,
				ChainHosts:   names,
			}
			if mc.originalChainState.OriginalHost == "" {
				mc.originalChainState.OriginalHost = strings.Join(names, " -> ")
			}
			mc.failState = StateRecovery
			connState.SetFailState(StateRecovery)
			mc.lastOrigHostCheck = time.Time{} // check immediately
			debugLog("MONITOR", "Priority is a chain (%s), handing over to Recovery", priorityHost)
			return
		}
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
				// NOTE: deliberately NOT adopting the new gen here:
				// this path restarts monitoring (StartMonitoring:true),
				// so the current loop must exit as superseded.
				result, _ := establishConnection(ConnectOptions{
					Hosts:              []HostConfig{*priorityHostConfig},
					OriginalHost:       priorityHost,
					StopMonitoring:     true,
					KillExistingTunnel: true,
					EnableSystemProxy:  true,
					SaveLastHost:       true,
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
}

// enterFailoverState saves the original chain state and transitions to Failover.
func (mc *monitoringConfig) enterFailoverState(state *ProxyState) {
	// Save original state for recovery
	stateCopy := *state
	mc.originalChainState = &stateCopy
	mc.failoverAttempts = 0
	mc.socksFails = 0
	mc.failoverStart = time.Now()

	// Transition to Failover
	mc.failState = StateFailover
	connState.SetFailState(StateFailover)
	connState.SetActive(false)

	debugLog("MONITOR", "Entering Failover state from Normal (original host: %s)", state.Host)
	updateTrayStatusReconnecting(aliasForState(state), remoteForState(state))
	updateMenuState()
}

// refailoverSameChain tries to rebuild the failed chain as-is: the entry hop
// must answer ssh directly (first-hop rule), the rest is proven end-to-end
// by a pre-flight tunnel test. Via-only members are NOT checked directly —
// that would block recovery forever.
// Returns (rebuilt, gen); a non-zero gen must be adopted by the caller even
// when rebuild failed, because establishConnection moved the generation.
func (mc *monitoringConfig) refailoverSameChain(state *ProxyState) (bool, uint64) {
	orig := mc.originalChainState
	if orig == nil || !orig.IsChain || len(orig.ChainHosts) == 0 {
		return false, 0
	}
	hosts := parseSSHConfig(Config.Paths.SSHConfig)
	hostMap := make(map[string]HostConfig, len(hosts))
	for _, h := range hosts {
		hostMap[h.Name] = h
	}
	var chain []HostConfig
	for _, name := range orig.ChainHosts {
		h, ok := hostMap[name]
		if !ok {
			debugLog("MONITOR", "Refailover: host %q not found in SSH config", name)
			return false, 0
		}
		chain = append(chain, h)
	}
	// Entry hop must be directly reachable.
	if ok, _ := checkSSHConnectionWithTime(chain[0], Config.Paths.WorkDir); !ok {
		debugLog("MONITOR", "Refailover: entry host %s unreachable, skipping", chain[0].Name)
		return false, 0
	}
	sshKeyPath := resolveSSHKeyPath(Config.Paths.WorkDir, chain[0].IdentityFile)
	if !ensureSSHAgent(sshKeyPath, loadSSHKeyPassphrase()) {
		debugLog("MONITOR", "Refailover: SSH-Agent failed")
		return false, 0
	}
	var chainNames []string
	for _, h := range chain {
		chainNames = append(chainNames, h.Name)
	}
	chainDisplay := strings.Join(chainNames, " -> ")
	testSSHCmd := buildSSHCommand(chain, sshKeyPath)
	testPort := Config.Network.ProxyPort + preflightPortChainRefail
	logTunnelEvent("INFO", chainDisplay,
		fmt.Sprintf("Refailover: pre-flight same-chain test on port %d...", testPort))
	if ok, freason := probeTunnelWithReason(testSSHCmd, testPort, preflightTimeout(len(chain))); !ok {
		if freason == "auth" {
			logTunnelEvent("ERROR", chainDisplay, "Refailover: SSH key rejected — manual fix needed")
			mc.enterErrorHalt(state, "SSH key rejected by "+chain[0].Name, false)
			return false, 0
		}
		debugLog("MONITOR", "Refailover: pre-flight FAILED for %s", chainDisplay)
		return false, 0
	}
	logTunnelEvent("INFO", chainDisplay, "Refailover: pre-flight PASSED — rebuilding same chain")
	result, gen := establishConnection(ConnectOptions{
		Hosts:              chain,
		IsChain:            true,
		OriginalHost:       orig.OriginalHost,
		IsFailoverActive:   true,
		FailoverStart:      time.Now().Format(time.RFC3339),
		StopMonitoring:     false, // don't stop ourselves
		KillExistingTunnel: true,
		EnableSystemProxy:  true,
		SaveLastHost:       false,
		StartMonitoring:    false, // don't start new monitoring
		UpdateTray:         true,
		DisplayAlias:       "Chain",
		DisplayTooltip:     chainDisplay,
	})
	if result == nil {
		logTunnelEvent("WARN", chainDisplay, "Refailover: rebuild failed, falling back to single host")
		return false, gen
	}
	logTunnelEvent("OK", chainDisplay, "Refailover: same chain rebuilt")
	updateTrayStatusFailover("Chain", chainDisplay)
	updateMenuState()
	mc.failState = StateRecovery
	connState.SetFailState(StateRecovery)
	mc.lastOrigHostCheck = time.Time{} // check original restore immediately
	mc.failoverAttempts = 0
	return true, gen
}

// handleFailoverState finds the fastest available host and connects to it.
// On success → transitions to Recovery.
// If all hosts fail → infinite retry with backoff (ErrorHalt only when
// the config yields no candidates at all).
// Returns the generation of the new session (0 = no session created).
// The caller MUST adopt a non-zero gen, including on connection failure —
// establishConnection bumps the generation even when the tunnel fails,
// and without adoption the monitoring loop would exit on the next tick.
func (mc *monitoringConfig) handleFailoverState(state *ProxyState) uint64 {
	debugLog("MONITOR", "handleFailoverState: attempt %d", mc.failoverAttempts+1)

	// SmartFailover=false: автоматическое перепрыгивание выключено
	// конфигом — честно уходим в ErrorHalt/NoInternet вместо тихого игнора
	// флага. Дефолт ini (true) поведение не меняет.
	if !Config.General.SmartFailover {
		mc.enterErrorHalt(state, "SmartFailover disabled by config — automatic host switching off", false)
		return 0
	}

	// Give up an endless loop: minutes of total failure → ErrorHalt/NoInternet
	// (which shows red), instead of yellow reconnecting forever.
	if failoverExpired(mc.failoverStart, time.Now()) {
		mc.enterErrorHalt(state, fmt.Sprintf("failover gave up after %v without reachable host", MaxFailoverDuration), true)
		return 0
	}

	// Same-chain re-establish first: if we fell off a chain, try to rebuild
	// the SAME chain (entry-direct + end-to-end preflight) before dropping
	// to a single host.
	if mc.originalChainState != nil && mc.originalChainState.IsChain && len(mc.originalChainState.ChainHosts) > 0 {
		if ok, gen := mc.refailoverSameChain(state); ok {
			debugLog("MONITOR", "Same-chain refailover succeeded, adopting gen %d", gen)
			return gen
		} else if gen != 0 {
			// Preflight/connection moved the generation — adopt it.
			return gen
		}
		// Chain not rebuildable right now — fall through to single-host failover.
	}

	// ── 1. Gather available hosts (all groups, exclude current and reverse) ──
	hosts := parseSSHConfig(Config.Paths.SSHConfig)
	if len(hosts) == 0 {
		mc.enterErrorHalt(state, "No hosts found in SSH config", false)
		return 0
	}

	// Determine which hosts to exclude: every member of the failed
	// original (single name or full chain set — see helper).
	excludeSet := buildFailoverExcludeSet(mc.originalChainState)

	var availableHosts []HostConfig
	for _, h := range hosts {
		// Exclude the host/chain members that just failed
		if excludeSet[h.Name] {
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
		mc.enterErrorHalt(state, "No alternative hosts available", true)
		return 0
	}

	// ── 2. Приоритет выше скорости (по анкете): сначала пробуем priorityHost ──
	// NOTE: single-name priority only. A chain priority ("a|b|c") never
	// matches a member name here on purpose: failover stays single-host,
	// the chain is restored later by the Recovery machinery.
	var fastestHost *HostConfig
	var responseTime time.Duration
	if hasPriorityHost && priorityHost != "" && !strings.Contains(priorityHost, "|") {
		for i := range availableHosts {
			if availableHosts[i].Name == priorityHost {
				if ok, rt := checkSSHConnectionWithTime(availableHosts[i], Config.Paths.WorkDir); ok {
					fastestHost = &availableHosts[i]
					responseTime = rt
					logTunnelEvent("INFO", state.Host,
						fmt.Sprintf("Priority host %s available (%.2fs), preferring over fastest", priorityHost, rt.Seconds()))
				} else {
					debugLog("MONITOR", "Priority host %s unavailable, falling back to fastest", priorityHost)
				}
				break
			}
		}
	}
	if fastestHost == nil {
		logTunnelEvent("INFO", state.Host, fmt.Sprintf("Looking for fastest among %d available hosts...", len(availableHosts)))
		fastestHost, responseTime = findFastestAvailableHost(availableHosts, Config.Paths.WorkDir)
	}

	if fastestHost == nil {
		mc.failoverAttempts++
		// Бесконечный бэкофф по анкете: не уходим в ErrorHalt, ждём следующий тик.
		backoff := failoverBackoff(mc.failoverAttempts)
		logTunnelEvent("WARN", state.Host,
			fmt.Sprintf("No reachable hosts (attempt %d), retry in %v", mc.failoverAttempts, backoff))
		debugLog("MONITOR", "Failover: no hosts, attempt=%d backoff=%v", mc.failoverAttempts, backoff)
		return 0
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
		mc.failoverAttempts++
		logTunnelEvent("ERROR", fastestHost.Name,
			fmt.Sprintf("SSH-Agent failed (attempt %d), retry in %v", mc.failoverAttempts, failoverBackoff(mc.failoverAttempts)))
		return 0
	}

	testSSHCmd := buildSSHCommand([]HostConfig{*fastestHost}, sshKeyPath)
	testPort := Config.Network.ProxyPort + 1000 // use a port far from the production one

	logTunnelEvent("INFO", fastestHost.Name,
		fmt.Sprintf("Pre-flight tunnel test on port %d...", testPort))

	if ok, freason := probeTunnelWithReason(testSSHCmd, testPort, 20*time.Second); !ok {
		if freason == "auth" {
			logTunnelEvent("ERROR", fastestHost.Name, "Pre-flight FAILED: SSH key rejected — manual fix needed")
			mc.enterErrorHalt(state, "SSH key rejected by "+fastestHost.Name, false)
			return 0
		}
		mc.failoverAttempts++
		backoff := failoverBackoff(mc.failoverAttempts)
		logTunnelEvent("WARN", fastestHost.Name,
			fmt.Sprintf("Pre-flight FAILED (attempt %d), retry in %v", mc.failoverAttempts, backoff))
		return 0
	}

	logTunnelEvent("INFO", fastestHost.Name,
		"Pre-flight test PASSED — safe to switch")

	// ── 4. Connect to failover host (DON'T stop/start monitoring) ──
	originalHost := state.Host
	if mc.originalChainState != nil {
		originalHost = mc.originalChainState.OriginalHost
	}

	result, gen := establishConnection(ConnectOptions{
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
		notifyUser("GoProxy failover", fmt.Sprintf("Switched to %s", fastestHost.Name))

		// Transition to Recovery — we're now on a failover host,
		// periodically checking if the original chain can be restored.
		mc.failState = StateRecovery
		connState.SetFailState(StateRecovery)
		mc.lastOrigHostCheck = time.Time{} // reset to check immediately
		mc.failoverAttempts = 0

		debugLog("MONITOR", "Failover → Recovery (failover host: %s)", fastestHost.Name)
		return gen
	}

	// Connection to this host failed — бесконечный retry, try again next tick.
	// Adopt gen anyway: the session was created, our generation moved.
	mc.failoverAttempts++
	backoff := failoverBackoff(mc.failoverAttempts)
	logTunnelEvent("ERROR", fastestHost.Name,
		fmt.Sprintf("Failed to connect (attempt %d), retry in %v", mc.failoverAttempts, backoff))
	return gen
}

// handleRecoveryState monitors the failover host and periodically checks
// if all original chain hosts are available for restoration.
// When all original hosts are verified → re-establishes original chain → Normal.
// If failover host drops → back to Failover.
// Returns the generation of a new session (0 = none created); the caller
// MUST adopt a non-zero gen for the same reason as in handleFailoverState.
func (mc *monitoringConfig) handleRecoveryState(state *ProxyState) uint64 {
	// ── 1. Check failover host (SOCKS5, debounced) ──
	if time.Since(mc.lastSocksCheck) >= mc.socksCheckInterval {
		mc.lastSocksCheck = time.Now()

		if !checkProxyConnectivityFunc() {
			if mc.recordSocks(false) {
				// Failover host itself dropped — return to Failover state
				logTunnelEvent("ERROR", state.Host, "Failover host lost connection, returning to Failover state")
				mc.failState = StateFailover
				connState.SetFailState(StateFailover)
				connState.SetActive(false)
				mc.failoverAttempts = 0
				updateTrayStatusReconnecting(aliasForState(state), remoteForState(state))
				updateMenuState()
			} else {
				debugLog("MONITOR", "Recovery: SOCKS flap (%d/%d), waiting", mc.socksFails, SocksFailThreshold)
			}
			return 0
		}
		mc.recordSocks(true)
	}

	// ── 2. Periodically check if original chain can be restored ──
	// ReturnToOriginalHost=false: остаёмся на failover-хосте (SOCKS-надзор
	// выше продолжает его сторожить). Дефолт ini (true) поведение не меняет.
	if !Config.General.ReturnToOriginalHost {
		return 0
	}
	if mc.origHostCheckInterval == 0 || time.Since(mc.lastOrigHostCheck) < mc.origHostCheckInterval {
		return 0
	}
	mc.lastOrigHostCheck = time.Now()

	if mc.originalChainState == nil {
		// No original state saved — nothing to recover
		return 0
	}

	origState := mc.originalChainState

	if origState.IsChain && len(origState.ChainHosts) > 0 {
		// ── Chain recovery: only the ENTRY hop must answer directly
		// (first-hop rule). Via-only members are deliberately NOT checked
		// directly — the pre-flight inside attemptChainRecovery proves
		// the whole chain end-to-end.
		entryName := origState.ChainHosts[0]
		hosts := parseSSHConfig(Config.Paths.SSHConfig)
		var entryConfig *HostConfig
		for i := range hosts {
			if hosts[i].Name == entryName {
				entryConfig = &hosts[i]
				break
			}
		}
		if entryConfig == nil {
			debugLog("MONITOR", "Recovery: entry host %q not found in SSH config", entryName)
			return 0
		}
		if ok, _ := checkSSHConnectionWithTime(*entryConfig, Config.Paths.WorkDir); !ok {
			debugLog("MONITOR", "Recovery: entry host %s unreachable, skipping restore", entryName)
			return 0
		}
		logTunnelEvent("INFO", state.Host,
			fmt.Sprintf("Entry host %s is reachable, attempting chain restoration", entryName))
		ok, newGen := attemptChainRecovery(origState)
		if ok {
			// Chain restored — transition to Normal
			mc.failState = StateNormal
			connState.SetFailState(StateNormal)
			connState.SetActive(true)
			mc.originalChainState = nil // clear saved state

			logTunnelEvent("OK", origState.Host, "Original chain restored — returning to Normal state")
			updateMenuState()
			return newGen
		}
		// Failed after pre-flight passed — stay in Recovery, but adopt:
		// the session was created, our generation moved.
		if newGen != 0 {
			return newGen
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
			return 0
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
				return 0
			}

			testSSHCmd := buildSSHCommand([]HostConfig{*origConfig}, origSSHKeyPath)
			testPort := Config.Network.ProxyPort + 1002 // separate from other test ports

			logTunnelEvent("INFO", origName,
				fmt.Sprintf("Pre-flight restore test on port %d (failover tunnel stays alive)...", testPort))

			if ok, freason := probeTunnelWithReason(testSSHCmd, testPort, 20*time.Second); !ok {
				if freason == "auth" {
					logTunnelEvent("ERROR", origName,
						"Pre-flight restore test FAILED: SSH key rejected — fix the key, failover tunnel stays up")
				} else {
					logTunnelEvent("WARN", origName,
						"Pre-flight restore test FAILED — failover tunnel preserved, will retry later")
				}
				return 0
			}

			logTunnelEvent("INFO", origName,
				"Pre-flight restore test PASSED — switching from failover to original")

			// Safe to switch now: kill failover tunnel and restore original
			result, gen := establishConnection(ConnectOptions{
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
				return gen
			}
			// Failed after pre-flight passed — stay in Recovery, but adopt:
			// the session was created, our generation moved.
			logTunnelEvent("WARN", origName, "Failed to return to original host after pre-flight passed, staying in Recovery")
			return gen
		} else {
			debugLog("MONITOR", "Recovery: original host %s still unavailable", origName)
		}
	}

	return 0
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
		return
	}

	// Retryable halt (transient outage, not a manual stop): periodically
	// attempt recovery instead of wedging red forever.
	if mc.errorHaltRetry && time.Since(mc.errorHaltStart) > ErrorHaltRetryAfter {
		logTunnelEvent("INFO", state.Host, "Error/Halt retry: attempting recovery")
		mc.failState = StateFailover
		connState.SetFailState(StateFailover)
		mc.failoverStart = time.Now()
		mc.failoverAttempts = 0
		mc.errorHaltRetry = false
		updateTrayStatusReconnecting(aliasForState(state), remoteForState(state))
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
			"NoInternet: 24h wait expired — entering Error/Halt")
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

	// Throttle the per-minute jabber: INFO on 10-minute marks, debug otherwise.
	if m := int(elapsed.Minutes()); m%10 == 0 {
		logTunnelEvent("INFO", state.Host,
			fmt.Sprintf("NoInternet: checking internet (%.0f min elapsed)...", elapsed.Minutes()))
	} else {
		debugLog("MONITOR", "NoInternet: checking internet (%.0f min elapsed)...", elapsed.Minutes())
	}

	alias := aliasForState(state)
	remote := remoteForState(state)
	if noInternetShouldBeRed(mc.noInternetStart, time.Now()) {
		updateTrayStatusNoInternet(alias, remote, elapsed, mc.noInternetMax)
	} else {
		// Grace period: yellow "waiting", red only when the outage persists.
		updateTrayStatusWaiting(alias, remote,
			fmt.Sprintf("Waiting for internet (%dm)...", int(elapsed.Minutes())+1))
	}

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
// retryable=true means the halt may resolve on its own (transient outage):
// handleFatalErrorState will periodically retry instead of wedging red forever.
func (mc *monitoringConfig) enterErrorHalt(state *ProxyState, reason string, retryable bool) {
	mc.errorHaltRetry = retryable
	mc.errorHaltStart = time.Now()
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
	// Start yellow ("waiting") — red comes after the grace period.
	updateTrayStatusWaiting(alias, remote, "Waiting for internet...")
	updateMenuState()
}

// isHealthyGenerate204 validates a generate_204-style probe response.
// Status must be exactly 204 with a (near-)empty body — captive portals
// answer 200/302 with a login page, which must NOT count as "internet".
func isHealthyGenerate204(status int, bodyLen int64) bool {
	return status == http.StatusNoContent && bodyLen >= 0 && bodyLen < 1024
}

// checkInternetDirect checks internet connectivity without using the proxy.
// URLs берутся из Config.Network.InternetCheckURLs (по анкете).
func checkInternetDirect() bool {
	testURLs := internetCheckURLs()

	client := &http.Client{Timeout: 5 * time.Second}
	for _, url := range testURLs {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		if isHealthyGenerate204(resp.StatusCode, int64(len(body))) {
			return true
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
	// gen берём из единого менеджера (уже инкрементирован в establishConnection)
	myGeneration := connMgr.generation()
	monitoringMutex.Unlock()

	defer func() {
		monitoringMutex.Lock()
		// Orphaned loop must not clear a newer session's flag:
		// only the current generation owns monitoringActive.
		if connMgr.generation() == myGeneration {
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

	// Restarting on top of a failover tunnel (tray restart, crash recovery,
	// NoInternet auto-connect): rebuild the pre-failover original from disk
	// and go straight to Recovery. Without this the original is forgotten
	// and the next drop would enshrine the failover host as "original".
	if state.IsFailoverActive {
		if orig := restoreOriginalFromDisk(state); orig != nil {
			mc.originalChainState = orig
			mc.failState = StateRecovery
			connState.SetFailState(StateRecovery)
			mc.lastOrigHostCheck = time.Time{} // check immediately
			logTunnelEvent("INFO", state.Host,
				fmt.Sprintf("Monitoring restored on failover host, will return to %s when available", orig.OriginalHost))
		}
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-monitoringStopChan:
			logTunnelEvent("INFO", state.Host, "Monitoring stopped")
			return

		case <-ticker.C:
			// Sleep/wake or clock jump: a long gap makes all in-flight probes
			// meaningless — reset debounce and skip this tick, re-verify on next.
			nowTick := time.Now()
			if sleepGapExceeded(mc.lastTick, nowTick) {
				debugLog("MONITOR", "Long tick gap (%v) — possible sleep/wake, resetting debounce", nowTick.Sub(mc.lastTick))
				mc.socksFails = 0
				mc.lastTick = nowTick
				mc.lastSocksCheck = nowTick
				continue
			}
			mc.lastTick = nowTick
			// Сессия superseded новым соединением/kill — выходим, но громко:
			// молчаливый выход здесь когда-то хоронил мониторинг после
			// failover-коннекта изнутри собственного тика (см. adopt ниже).
			if connMgr.generation() != myGeneration {
				debugLog("MONITOR", "Generation changed, monitoring exiting (gen %d -> %d)", myGeneration, connMgr.generation())
				logTunnelEvent("WARN", state.Host,
					fmt.Sprintf("Monitoring session superseded (gen %d -> %d), exiting", myGeneration, connMgr.generation()))
				return
			}
			// Check user-initiated stop (handleKillProxy sets this flag)
			if atomic.LoadUint64(&userStopRequested) != 0 {
				logTunnelEvent("INFO", state.Host, "User stop requested, monitoring exiting")
				return
			}

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
				// Adopt: the failover connection created a new session
				// inside our own tick — it is ours, keep monitoring it.
				if newGen := mc.handleFailoverState(state); newGen != 0 {
					debugLog("MONITOR", "Adopting generation %d -> %d (failover)", myGeneration, newGen)
					myGeneration = newGen
				}
				// After failover connection, reload state from disk
				if s, err := LoadState(); err == nil {
					state = s
				}

			case StateRecovery:
				// Adopt: same as failover — the restore connection
				// created a new session inside our own tick.
				if newGen := mc.handleRecoveryState(state); newGen != 0 {
					debugLog("MONITOR", "Adopting generation %d -> %d (recovery)", myGeneration, newGen)
					myGeneration = newGen
				}
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
		var ghosts []string
		for _, name := range chainNames {
			found := false
			for _, host := range hosts {
				if host.Name == name {
					chain = append(chain, host)
					found = true
					break
				}
			}
			if !found {
				ghosts = append(ghosts, name)
			}
		}
		if len(ghosts) > 0 {
			logTunnelEvent("WARN", priorityHostName,
				fmt.Sprintf("Auto-connect: hosts not in SSH config (ghosts): %v — skipping them", ghosts))
		}

		if len(chain) > 0 {
			// Check if first host in chain is available
			available, _ := checkSSHConnectionWithTime(chain[0], Config.Paths.WorkDir)
			if !available {
				debugLog("MONITOR", "Priority chain first host %s unavailable, looking for fallback", chain[0].Name)
				// Find fastest available host
				var availableHosts []HostConfig
				for _, h := range hosts {
					if h.Group == hosts[0].Group && !isReverseHost(h) && h.ProxyJump == "" {
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
						if atomic.LoadUint64(&userStopRequested) != 0 {
							debugLog("MONITOR", "Auto-connect aborted: user stopped, skipping establish")
							return
						}
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
					if atomic.LoadUint64(&userStopRequested) != 0 {
						debugLog("MONITOR", "Auto-connect aborted: user stopped, skipping establish")
						return
					}
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
			logTunnelEvent("WARN", priorityHostName,
				"Auto-connect: priority host not found in SSH config (ghost) — skipping auto-connect")
			return
		}

		available, _ := checkSSHConnectionWithTime(*priorityHostConfig, Config.Paths.WorkDir)
		if !available {
			debugLog("MONITOR", "Priority host %s unavailable, looking for fallback", priorityHostName)
			// Find fastest available host
			var availableHosts []HostConfig
			for _, h := range hosts {
				if h.Group == hosts[0].Group && h.Name != priorityHostName && !isReverseHost(h) && h.ProxyJump == "" {
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
					if atomic.LoadUint64(&userStopRequested) != 0 {
						debugLog("MONITOR", "Auto-connect aborted: user stopped, skipping establish")
						return
					}
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
				if atomic.LoadUint64(&userStopRequested) != 0 {
					debugLog("MONITOR", "Auto-connect aborted: user stopped, skipping establish")
					return
				}
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
