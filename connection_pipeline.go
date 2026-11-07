// connection_pipeline.go
// Centralizes the SSH tunnel connection pipeline.
// All connection paths (user click, smart failover, return-to-original,
// chain, interactive, reconnection) go through establishConnection()
// to eliminate the 7× duplicated code pattern.
package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// establishMu serializes whole connection attempts: no two tunnels are
// built/killed concurrently (fast double clicks, monitor + user racing).
// establishBusyFlag is set when an attempt backs off because another one
// is already in flight (TryLock failed). UI reads it via establishWasBusy.
var establishMu sync.Mutex
var establishBusyFlag int64

// establishWasBusy reports whether the last establishConnection call found
// another one in flight (used for the "please wait" tray hint).
func establishWasBusy() bool {
	return atomic.LoadInt64(&establishBusyFlag) != 0
}

// ConnectOptions parametrizes a connection attempt.
type ConnectOptions struct {
	// Hosts in the connection chain (1 element = single host, N = chain).
	Hosts []HostConfig

	// Connection metadata
	IsChain          bool   // true if Hosts has > 1 hop
	OriginalHost     string // preserved across failover for return-to-original
	IsFailoverActive bool   // true if this is a failover host
	FailoverStart    string // RFC3339 timestamp when failover began

	// Behaviour flags
	StopMonitoring     bool // call stopMonitoring() before connecting
	KillExistingTunnel bool // kill existing SSH tunnel process
	EnableSystemProxy  bool // set system proxy via PAC
	SaveLastHost       bool // persist host name for auto-connect
	StartMonitoring    bool // launch background monitoring goroutine
	UpdateTray         bool // update tray icon/title/menu
	InteractiveMode    bool // skip tray UI updates (interactive console mode)

	// Identity — which SSH key to use.  Auto-detected from Hosts[0] if empty.
	SSHKeyPath string
	// Override SSH command.  Auto-built from Hosts if empty.
	SSHCommand []string

	// Preflight — prove the new tunnel on a test port BEFORE killing
	// the live one. On failure the old tunnel (and its monitoring)
	// stay untouched and nil is returned with gen 0.
	Preflight bool

	// Display names for tray/logging (auto-derived if empty).
	DisplayAlias   string // e.g. "myserver" or "Chain"
	DisplayTooltip string // e.g. "user@host.com"
}

// establishConnection runs the full connection pipeline:
//  1. Stop monitoring & kill existing tunnel (if requested)
//  2. Load SSH key passphrase & ensure ssh-agent
//  3. Build (or use provided) SSH command
//  4. Start SSH tunnel (with retries for chains)
//  5. Build ProxyState & persist
//  6. Start PAC server & set system proxy (if requested)
//  7. Update connState, menu & tray (if requested)
//  8. Start monitoring (if requested)
//
// Returns the ProxyState on success (nil on failure) plus the generation of
// the new session. The gen is returned on BOTH paths: newSession() already
// bumped it before any failure can happen. A monitoring loop that triggered
// this connection from inside its own tick MUST adopt the gen — otherwise
// its next tick sees a mismatch and exits, leaving the tunnel unmonitored.
func establishConnection(opts ConnectOptions) (*ProxyState, uint64) {
	// Serialize whole attempts — no parallel tunnel build/kill races.
	// A racing second attempt backs off immediately instead of queueing
	// behind a 45s pre-flight (UI shows "please wait" via establishWasBusy).
	atomic.StoreInt64(&establishBusyFlag, 0)
	if !establishMu.TryLock() {
		logTunnelEvent("WARN", chainDisplayOf(opts.Hosts), "Connection attempt skipped: another switch is already in progress")
		atomic.StoreInt64(&establishBusyFlag, 1)
		return nil, 0
	}
	defer establishMu.Unlock()

	// Clear the user-stop flag: any new connection attempt (user click,
	// failover, recovery) cancels a previous "Stop Proxy" request.
	atomic.StoreUint64(&userStopRequested, 0)

	// Pre-flight: prove the new tunnel on a test port BEFORE invalidating
	// anything. On failure the live tunnel and its monitoring survive
	// untouched (gen 0 — the caller must not adopt anything).
	if opts.Preflight {
		if !preflightNewTunnel(opts) {
			showTrayConnectionFailed()
			return nil, 0
		}
	}

	// Invalidate any previous connection attempts (cancel zombie retry loops)
	// Единая точка: отменяет старый мониторинг/retry через connMgr.
	gen, _ := connMgr.newSession()
	debugLog("PIPELINE", "establishConnection: %d hosts, isChain=%v, gen=%d", len(opts.Hosts), opts.IsChain, gen)
	// ── 1. Pre-connect cleanup ──────────────────────────────────────────
	if opts.StopMonitoring {
		stopMonitoring()
	}
	if opts.KillExistingTunnel {
		killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
		// PID-файла может не быть (чистка stop, краш) — добиваем по порту,
		// иначе сирота останется вторым листенером на :1080.
		killStaleListeners(Config.Network.ProxyPort)
		time.Sleep(500 * time.Millisecond)
	}

	// ── 2. Resolve SSH key & load into agent ────────────────────────────
	sshKeyPath := opts.SSHKeyPath
	if sshKeyPath == "" && len(opts.Hosts) > 0 {
		sshKeyPath = resolveSSHKeyPath(Config.Paths.WorkDir, opts.Hosts[0].IdentityFile)
	}
	sshKeyPass := loadSSHKeyPassphrase()
	if !ensureSSHAgent(sshKeyPath, sshKeyPass) {
		logTunnelEvent("ERROR", "SSH-Agent", "Failed to load SSH key into agent — aborting connection")
		return nil, gen
	}
	debugLog("PIPELINE", "SSH key in agent: %s", sshKeyPath)

	// ── 3. Build SSH command ────────────────────────────────────────────
	sshCmd := opts.SSHCommand
	if len(sshCmd) == 0 {
		sshCmd = buildSSHCommand(opts.Hosts, sshKeyPath)
	}
	debugLog("PIPELINE", "SSH command built: %v", sshCmd)

	// ── 4. Derive display names ────────────────────────────────────────
	alias := opts.DisplayAlias
	tooltip := opts.DisplayTooltip

	if !opts.IsChain && len(opts.Hosts) == 1 {
		if alias == "" {
			alias = opts.Hosts[0].Name
		}
		if tooltip == "" {
			tooltip = opts.Hosts[0].HostName
		}
	} else if opts.IsChain && alias == "" {
		alias = "Chain"
		var names []string
		for _, h := range opts.Hosts {
			names = append(names, h.Name)
		}
		chainStr := fmt.Sprintf("%s -> %s", opts.Hosts[0].HostName, opts.Hosts[len(opts.Hosts)-1].HostName)
		if tooltip == "" {
			tooltip = chainStr
		}
		if opts.DisplayAlias == "" {
			alias = "Chain"
		}
	}

	// ── 5. Start tunnel ────────────────────────────────────────────────
	chainLen := len(opts.Hosts)
	if chainLen == 0 {
		chainLen = 1
	}

	tunnelOK := startSSHTunnelWithRetries(sshCmd, chainLen)
	debugLog("PIPELINE", "Tunnel start result: %v", tunnelOK)
	if !tunnelOK {
		hostLabel := alias
		if hostLabel == "" {
			hostLabel = "unknown"
		}
		logTunnelEvent("ERROR", hostLabel, "Connection failed")

		if opts.UpdateTray && !opts.InteractiveMode {
			showTrayConnectionFailed()
		}
		return nil, gen
	}

	// ── 6. Build & persist ProxyState ──────────────────────────────────
	hostName := alias
	if opts.IsChain && len(opts.Hosts) > 0 {
		var chainNames []string
		for _, h := range opts.Hosts {
			chainNames = append(chainNames, h.Name)
		}
		hostName = fmt.Sprintf("%s -> %s", chainNames[0], chainNames[len(chainNames)-1])
		// Store full chain display
		fullChain := ""
		for i, n := range chainNames {
			if i > 0 {
				fullChain += " -> "
			}
			fullChain += n
		}

		state := ProxyState{
			IsChain:          true,
			Host:             fullChain,
			OriginalHost:     opts.OriginalHost,
			IsFailoverActive: opts.IsFailoverActive,
			FailoverStart:    opts.FailoverStart,
			ChainHosts:       chainNames,
			ProxyPort:        Config.Network.ProxyPort,
			KeyPath:          sshKeyPath,
			SSHCommand:       sshCmd,
			RemoteHost:       opts.Hosts[len(opts.Hosts)-1].HostName,
		}
		SaveState(state)
		debugLog("PIPELINE", "State saved (chain: %s)", fullChain)
		if opts.SaveLastHost {
			SaveLastHost(strings.Join(chainNames, "|"))
		}
		return finishConnection(state, alias, tooltip, opts), gen
	}

	// Single host path
	if opts.OriginalHost == "" {
		opts.OriginalHost = hostName
	}

	state := ProxyState{
		IsChain:          false,
		Host:             hostName,
		OriginalHost:     opts.OriginalHost,
		IsFailoverActive: opts.IsFailoverActive,
		FailoverStart:    opts.FailoverStart,
		ProxyPort:        Config.Network.ProxyPort,
		KeyPath:          sshKeyPath,
		SSHCommand:       sshCmd,
		RemoteHost:       tooltip,
	}
	SaveState(state)
	debugLog("PIPELINE", "State saved (single: %s)", hostName)
	if opts.SaveLastHost {
		SaveLastHost(hostName)
	}

	return finishConnection(state, alias, tooltip, opts), gen
}

// preflightNewTunnel builds the SSH command and proves a full SOCKS5 tunnel
// on a temporary port. It never touches the live tunnel, PID files or the
// monitoring generation — safe to run before the handover.
func preflightNewTunnel(opts ConnectOptions) bool {
	if len(opts.Hosts) == 0 {
		return false
	}
	sshKeyPath := opts.SSHKeyPath
	if sshKeyPath == "" {
		sshKeyPath = resolveSSHKeyPath(Config.Paths.WorkDir, opts.Hosts[0].IdentityFile)
	}
	if !ensureSSHAgent(sshKeyPath, loadSSHKeyPassphrase()) {
		logTunnelEvent("ERROR", opts.Hosts[0].Name, "Pre-flight: SSH-Agent failed — aborting")
		return false
	}
	sshCmd := opts.SSHCommand
	if len(sshCmd) == 0 {
		sshCmd = buildSSHCommand(opts.Hosts, sshKeyPath)
	}
	testPort := Config.Network.ProxyPort + preflightPortUserChain
	timeout := preflightTimeout(len(opts.Hosts))
	logTunnelEvent("INFO", opts.Hosts[0].Name,
		fmt.Sprintf("Pre-flight tunnel test on port %d (live tunnel stays up)...", testPort))
	if ok, freason := probeTunnelWithReason(sshCmd, testPort, timeout); !ok {
		if freason == "auth" {
			logTunnelEvent("ERROR", opts.Hosts[0].Name, "Pre-flight FAILED: SSH key rejected — manual fix needed, not retrying")
		} else {
			logTunnelEvent("WARN", opts.Hosts[0].Name, "Pre-flight test FAILED — keeping live tunnel")
		}
		return false
	}
	logTunnelEvent("OK", opts.Hosts[0].Name, "Pre-flight test PASSED")
	return true
}

// finishConnection handles post-tunnel steps: PAC, proxy, state, tray, monitoring.
func finishConnection(state ProxyState, alias, tooltip string, opts ConnectOptions) *ProxyState {
	debugLog("PIPELINE", "finishConnection: %s", alias)
	// PAC + system proxy (only if the PAC server actually bound its port —
	// pointing the system at a dead PAC kills the user's internet).
	pacOK := true
	if opts.EnableSystemProxy {
		startPACServer()         // generate & write PAC file to disk
		startPACServerInternal() // start HTTP server to serve the PAC file
		if waitForPACServer(Config.Network.PACHttpPort, 3*time.Second) {
			pacURL := fmt.Sprintf("http://127.0.0.1:%d/x_proxy.pac", Config.Network.PACHttpPort)
			setSystemProxy(pacURL)
		} else {
			pacOK = false
			logTunnelEvent("ERROR", alias, fmt.Sprintf("PAC server failed to bind :%d (port busy?) — system proxy NOT enabled, tunnel still works", Config.Network.PACHttpPort))
		}
	}

	// Global connection state
	connState.SetConnected(state.Host)

	// Tray UI
	if opts.UpdateTray && !opts.InteractiveMode {
		updateMenuState()
		failoverSuffix := ""
		if opts.IsFailoverActive {
			failoverSuffix = " (Failover)"
		}
		updateTrayStatusOnline(alias, tooltip+failoverSuffix)
		if opts.EnableSystemProxy && !pacOK {
			flashTrayHint(fmt.Sprintf("PAC :%d busy — proxy NOT set", Config.Network.PACHttpPort),
				"Another app holds the PAC port. Tunnel works; system proxy left untouched.", 5*time.Second)
		}
	}

	// Monitoring
	if opts.StartMonitoring {
		go startMonitoring(&state)
	}

	return &state
}

// waitForPACServer polls until something accepts TCP on the PAC port
// (our freshly started server) or the timeout expires. Guards against
// enabling the system proxy toward a dead PAC (bind conflict).
func waitForPACServer(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if tcpPortOpen("127.0.0.1", strconv.Itoa(port), time.Second) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// showTrayConnectionFailed resets tray to yellow with "Connection failed".
func showTrayConnectionFailed() {
	updateTrayStatusReconnecting("?", "Connection failed")
}
