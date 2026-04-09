// connection_pipeline.go
// Centralizes the SSH tunnel connection pipeline.
// All connection paths (user click, smart failover, return-to-original,
// chain, interactive, reconnection) go through establishConnection()
// to eliminate the 7× duplicated code pattern.
package main

import (
        "fmt"
        "strings"
        "time"
)

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
        StopMonitoring      bool // call stopMonitoring() before connecting
        KillExistingTunnel  bool // kill existing SSH tunnel process
        EnableSystemProxy   bool // set system proxy via PAC
        SaveLastHost        bool // persist host name for auto-connect
        StartMonitoring     bool // launch background monitoring goroutine
        UpdateTray          bool // update tray icon/title/menu
        InteractiveMode     bool // skip tray UI updates (interactive console mode)

        // Identity — which SSH key to use.  Auto-detected from Hosts[0] if empty.
        SSHKeyPath string
        // Override SSH command.  Auto-built from Hosts if empty.
        SSHCommand []string

        // Display names for tray/logging (auto-derived if empty).
        DisplayAlias   string // e.g. "myserver" or "Chain"
        DisplayTooltip string // e.g. "user@host.com"
}

// establishConnection runs the full connection pipeline:
//   1. Stop monitoring & kill existing tunnel (if requested)
//   2. Load SSH key passphrase & ensure ssh-agent
//   3. Build (or use provided) SSH command
//   4. Start SSH tunnel (with retries for chains)
//   5. Build ProxyState & persist
//   6. Start PAC server & set system proxy (if requested)
//   7. Update connState, menu & tray (if requested)
//   8. Start monitoring (if requested)
//
// Returns the ProxyState on success, nil on failure.
func establishConnection(opts ConnectOptions) *ProxyState {
        debugLog("PIPELINE", "establishConnection: %d hosts, isChain=%v", len(opts.Hosts), opts.IsChain)
        // ── 1. Pre-connect cleanup ──────────────────────────────────────────
        if opts.StopMonitoring {
                stopMonitoring()
        }
        if opts.KillExistingTunnel {
                killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
                time.Sleep(500 * time.Millisecond)
        }

        // ── 2. Resolve SSH key & load into agent ────────────────────────────
        sshKeyPath := opts.SSHKeyPath
        if sshKeyPath == "" && len(opts.Hosts) > 0 {
                sshKeyPath = resolveSSHKeyPath(Config.Paths.WorkDir, opts.Hosts[0].IdentityFile)
        }
        sshKeyPass := loadSSHKeyPassphrase()
        ensureSSHAgent(sshKeyPath, sshKeyPass)
        debugLog("PIPELINE", "SSH key loaded into agent: %s", sshKeyPath)

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
                return nil
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
                return finishConnection(state, alias, tooltip, opts)
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

        return finishConnection(state, alias, tooltip, opts)
}

// finishConnection handles post-tunnel steps: PAC, proxy, state, tray, monitoring.
func finishConnection(state ProxyState, alias, tooltip string, opts ConnectOptions) *ProxyState {
        debugLog("PIPELINE", "finishConnection: %s", alias)
        // PAC + system proxy
        if opts.EnableSystemProxy {
                startPACServer()
                pacURL := fmt.Sprintf("http://127.0.0.1:%d/x_proxy.pac", Config.Network.PACHttpPort)
                setSystemProxy(pacURL)
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
        }

        // Monitoring
        if opts.StartMonitoring {
                go startMonitoring(&state)
        }

        return &state
}

// showTrayConnectionFailed resets tray to yellow with "Connection failed".
func showTrayConnectionFailed() {
        updateTrayStatusReconnecting("?", "Connection failed")
}
