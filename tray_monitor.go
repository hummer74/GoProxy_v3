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

// startMonitoring starts a new monitoring loop for the current connection
func startMonitoring(state *ProxyState) {
	var originalHostCheckTimer *time.Timer

	monitoringMutex.Lock()
	if monitoringActive {
		monitoringMutex.Unlock()
		return // Already monitoring
	}
	monitoringActive = true
	monitoringMutex.Unlock()

	defer func() {
		monitoringMutex.Lock()
		monitoringActive = false
		monitoringMutex.Unlock()
		if originalHostCheckTimer != nil {
			originalHostCheckTimer.Stop()
		}
	}()

	// Clear any existing stop signals
	select {
	case <-monitoringStopChan:
	default:
	}

	// Configuration parameters - все теперь в секундах
	socksCheckInterval := time.Duration(Config.Network.SocksCheckInterval) * time.Second
	internetCheckDelay := time.Duration(Config.Network.InternetCheckDelay) * time.Second
	internetCheckRetry := time.Duration(Config.Network.InternetCheckRetry) * time.Second
	reconnectAttemptDelay := time.Duration(Config.Network.ReconnectAttemptDelay) * time.Second
	maxReconnectTime := time.Duration(Config.Network.MaxReconnectTime) * time.Second
	originalHostCheckInterval := time.Duration(Config.General.OriginalHostCheck) * time.Second

	// Таймер для проверки оригинального хоста
	if Config.General.ReturnToOriginalHost && state.IsFailoverActive {
		originalHostCheckTimer = time.NewTimer(originalHostCheckInterval)
	}

	// State variables for this monitoring session
	var (
		isReconnecting       bool
		isFatalError         bool
		reconnectStartTime   time.Time
		lastInternetCheck    time.Time
		lastReconnectAttempt time.Time
		lastSocksCheck       time.Time
		networkAvailable     bool = true
		reconnectAttempts    int
	)

	// Main monitoring loop
	for {
		select {
		case <-monitoringStopChan:
			monitoringMutex.Lock()
			monitoringActive = false
			monitoringMutex.Unlock()
			if originalHostCheckTimer != nil {
				originalHostCheckTimer.Stop()
			}
			logTunnelEvent("INFO", state.Host, "Monitoring stopped")
			return
		default:
			// Continue with monitoring
		}

		// Проверка таймера для оригинального хоста (если в режиме failover)
		if originalHostCheckTimer != nil {
			select {
			case <-originalHostCheckTimer.C:
				if Config.General.ReturnToOriginalHost && state.IsFailoverActive {
					if checkAndReturnToOriginalHostFunc(state) {
						// Успешно вернулись к оригинальному хосту, выходим из мониторинга
						if originalHostCheckTimer != nil {
							originalHostCheckTimer.Stop()
						}
						return
					}
					// Перезапускаем таймер
					originalHostCheckTimer.Reset(originalHostCheckInterval)
				}
			default:
				// Таймер еще не сработал
			}
		}

		// Check for stop flag from system
		if _, err := os.Stat(Config.TempFiles.StopFlag); err == nil {
			monitoringMutex.Lock()
			monitoringActive = false
			monitoringMutex.Unlock()
			if originalHostCheckTimer != nil {
				originalHostCheckTimer.Stop()
			}
			logTunnelEvent("INFO", state.Host, "Stop flag detected, monitoring stopped")
			return
		}

		// Handle fatal error state - only check SOCKS5 periodically
		if isFatalError {
			// In fatal error state, we just check SOCKS5 periodically
			if time.Since(lastSocksCheck) >= socksCheckInterval {
				lastSocksCheck = time.Now()
				if checkProxyConnectivityFunc() {
					// Tunnel recovered, exit fatal error state
					isFatalError = false
					isReconnecting = false
					reconnectAttempts = 0
					tunnelStartTime = time.Now()
					isTunnelActive = true
					logTunnelEvent("OK", state.Host, "Tunnel recovered from fatal error state")

					// Update tray status for chain
					if state.IsChain {
						updateTrayStatusOnline("Chain", state.Host)
					} else {
						updateTrayStatusOnline(state.Host, state.RemoteHost)
					}

					updateMenuState()
				}
			}
			time.Sleep(1 * time.Second)
			continue
		}

		// Normal monitoring - check SOCKS5 connectivity
		if time.Since(lastSocksCheck) >= socksCheckInterval {
			lastSocksCheck = time.Now()

			if checkProxyConnectivityFunc() {
				// Tunnel is online
				if isReconnecting {
					// Successfully reconnected
					isReconnecting = false
					reconnectAttempts = 0
					tunnelStartTime = time.Now()
					isTunnelActive = true
					logTunnelEvent("OK", state.Host, "Tunnel reconnected successfully")
					updateMenuState()
				}
				// Keep status online
				continue
			} else {
				// Tunnel went offline
				if !isReconnecting {
					isReconnecting = true
					reconnectStartTime = time.Now()
					lastInternetCheck = time.Time{}
					lastReconnectAttempt = time.Time{}
					reconnectAttempts = 0
					isTunnelActive = false
					logTunnelEvent("ERROR", state.Host, "Tunnel lost connection")
					updateMenuState()

					// Проверяем, нужно ли использовать smart failover
					if Config.General.SmartFailover && !state.IsChain {
						// Пробуем умное переключение на другой хост
						if handleSmartFailover(state) {
							// Успешно переключились на другой хост, выходим из этого мониторинга
							if originalHostCheckTimer != nil {
								originalHostCheckTimer.Stop()
							}
							return
						}
					}

					// Update tray status for chain
					if state.IsChain {
						updateTrayStatusReconnecting("Chain", state.Host)
					} else {
						updateTrayStatusReconnecting(state.Host, state.RemoteHost)
					}

					disableSystemProxy()
					killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
				}
			}
		}

		// If not reconnecting, just wait for next check
		if !isReconnecting {
			time.Sleep(1 * time.Second)
			continue
		}

		// We are in reconnection state
		// Check max reconnection time
		if time.Since(reconnectStartTime) > maxReconnectTime {
			logTunnelEvent("ERROR", state.Host,
				fmt.Sprintf("Max reconnection time exceeded (%d seconds)", Config.Network.MaxReconnectTime))
			handleFatalError(state.Host, state.RemoteHost)
			isFatalError = true
			isReconnecting = false
			isTunnelActive = false
			updateMenuState()
			continue
		}

		// Check if we should wait for internet check delay
		if time.Since(reconnectStartTime) < internetCheckDelay {
			time.Sleep(1 * time.Second)
			continue
		}

		// Check internet connectivity (with retry interval)
		if time.Since(lastInternetCheck) >= internetCheckRetry {
			lastInternetCheck = time.Now()
			networkAvailable = checkInternetFunc()

			if !networkAvailable {
				logTunnelEvent("WARN", state.Host, "No internet connectivity detected")

				// Update tray status for chain
				if state.IsChain {
					updateTrayStatusNoInternet("Chain", state.Host, time.Since(reconnectStartTime), maxReconnectTime)
				} else {
					updateTrayStatusNoInternet(state.Host, state.RemoteHost, time.Since(reconnectStartTime), maxReconnectTime)
				}

				time.Sleep(1 * time.Second)
				continue
			}
		} else if !networkAvailable {
			// Still waiting for internet
			if state.IsChain {
				updateTrayStatusNoInternet("Chain", state.Host, time.Since(reconnectStartTime), maxReconnectTime)
			} else {
				updateTrayStatusNoInternet(state.Host, state.RemoteHost, time.Since(reconnectStartTime), maxReconnectTime)
			}

			time.Sleep(1 * time.Second)
			continue
		}

		// Internet is available, attempt to reconnect
		if time.Since(lastReconnectAttempt) >= reconnectAttemptDelay {
			lastReconnectAttempt = time.Now()
			reconnectAttempts++

			remainingTime := maxReconnectTime - time.Since(reconnectStartTime)

			logTunnelEvent("INFO", state.Host,
				fmt.Sprintf("Reconnection attempt %d (%.0f minutes remaining)",
					reconnectAttempts, remainingTime.Minutes()))

			if state.IsChain {
				updateTrayStatusAttempting("Chain", state.Host, reconnectAttempts, remainingTime)
			} else {
				updateTrayStatusAttempting(state.Host, state.RemoteHost, reconnectAttempts, remainingTime)
			}

			if attemptTunnelRestartFunc(state) {
				// Success!
				isReconnecting = false
				reconnectAttempts = 0
				tunnelStartTime = time.Now()
				isTunnelActive = true
				logTunnelEvent("OK", state.Host, "Tunnel reestablished after reconnection")

				// Update tray status for chain
				if state.IsChain {
					updateTrayStatusOnline("Chain", state.Host)
				} else {
					updateTrayStatusOnline(state.Host, state.RemoteHost)
				}

				updateMenuState()

				// Re-enable system proxy
				pacURL := fmt.Sprintf("http://127.0.0.1:%d/x_proxy.pac", Config.Network.PACHttpPort)
				setSystemProxy(pacURL)
				continue
			} else {
				logTunnelEvent("ERROR", state.Host,
					fmt.Sprintf("Reconnection attempt %d failed", reconnectAttempts))
			}
		}

		// Wait before next check
		time.Sleep(1 * time.Second)
	}
}

// stopMonitoring stops the current monitoring loop
func stopMonitoring() {
	monitoringMutex.Lock()
	defer monitoringMutex.Unlock()

	if monitoringActive {
		select {
		case monitoringStopChan <- true:
			// Signal sent successfully
		default:
			// Channel already has a signal
		}
		monitoringActive = false
	}
}

// checkAndRestoreExistingTunnel checks if a tunnel is already running and restores monitoring
func checkAndRestoreExistingTunnel() {
	// Проверяем, существует ли PID файл и процесс жив
	if !checkProcessRunning(Config.TempFiles.SSHTunnelPID) {
		// Нет туннеля – пытаемся автосоединение
		tryAutoConnectLastHost()
		return
	}

	// Туннель существует – даём ему время на установку
	// Ждём до 15 секунд, пока прокси не станет доступен
	maxWait := 15 * time.Second
	checkInterval := 500 * time.Millisecond
	start := time.Now()

	for {
		if checkProxyConnectivity() {
			// Прокси ответил – восстанавливаем состояние
			state, err := LoadState()
			if err == nil {
				currentHost = state.Host
				isTunnelActive = true
				tunnelStartTime = time.Now() // приблизительно
				updateMenuState()
				if state.IsChain {
					updateTrayStatusOnline("Chain", state.Host)
				} else {
					updateTrayStatusOnline(state.Host, state.RemoteHost)
				}
				go startMonitoring(state)
				logTunnelEvent("INFO", state.Host, "Restored monitoring for existing tunnel")
				return
			}
			// Если не удалось загрузить состояние – не страшно, просто выходим
			logTunnelEvent("WARN", "unknown", "Tunnel active but state missing")
			return
		}

		if time.Since(start) >= maxWait {
			// Прокси так и не ответил – вероятно, процесс завис
			logTunnelEvent("WARN", "unknown", "Existing tunnel not responding, killing process")
			killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
			break
		}
		time.Sleep(checkInterval)
	}

	// После убийства процесса пытаемся автосоединиться
	tryAutoConnectLastHost()
}

// tryAutoConnectLastHost attempts to auto-connect to the last used host
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
		// This is a chain, try to restore the chain
		chainNames := strings.Split(lastHost, "|")
		hosts := parseSSHConfig(Config.Paths.SSHConfig)

		if len(hosts) == 0 {
			return
		}

		// Find all hosts in the chain
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
			// Connect to the chain (auto‑connect, not manual)
			go func() {
				time.Sleep(2 * time.Second)         // Small delay for system to initialize
				handleChainConnection(chain, false) // false = auto‑connect
			}()
		}
	} else {
		// Single host (original behavior)
		hosts := parseSSHConfig(Config.Paths.SSHConfig)
		if len(hosts) == 0 {
			return
		}

		// Find the last host in config
		for _, host := range hosts {
			if host.Name == lastHost {
				// Check if it's in first group (for consistency)
				firstGroup := hosts[0].Group
				if host.Group == firstGroup {
					go func(h HostConfig) {
						time.Sleep(2 * time.Second) // Small delay for system to initialize
						handleHostClick(h)
					}(host)
				}
				break
			}
		}
	}
}

// handleChainConnection establishes connection to a chain of hosts (used by tray)
// manual: true when called from user click, false when called from auto‑connect
func handleChainConnection(chain []HostConfig, manual bool) {
	if len(chain) == 0 {
		return
	}

	// Check if we're already connected to this chain
	var chainNames []string
	for _, host := range chain {
		chainNames = append(chainNames, host.Name)
	}
	chainStr := strings.Join(chainNames, " -> ")

	if isTunnelActive && currentHost == chainStr {
		// Already connected to this chain, ignore
		return
	}

	if manual {
		logTunnelEvent("INFO", chainStr, fmt.Sprintf("User clicked to connect to chain: %s", chainStr))
	} else {
		logTunnelEvent("INFO", chainStr, "Auto-connecting to chain")
	}

	// Stop current monitoring if active
	stopMonitoring()

	// Kill any existing tunnel
	killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
	time.Sleep(500 * time.Millisecond) // Short delay to ensure process is killed

	// Connect to the selected chain

	// Load SSH-KEY-PASS from file
	sshKeyPass := loadSSHKeyPassphrase()

	// Resolve SSH-KEY path
	sshKeyPath := resolveSSHKeyPath(Config.Paths.WorkDir, chain[0].IdentityFile)

	// Load SSH-KEY into agent with SSH-KEY-PASS
	ensureSSHAgent(sshKeyPath, sshKeyPass)

	sshCmd := buildSSHCommand(chain, sshKeyPath)

	// Use startSSHTunnelWithRetries with correct chain length
	if startSSHTunnelWithRetries(sshCmd, len(chain)) {
		state := ProxyState{
			IsChain:          true,
			Host:             chainStr,
			OriginalHost:     chainStr,
			IsFailoverActive: false,
			ChainHosts:       chainNames,
			ProxyPort:        Config.Network.ProxyPort,
			KeyPath:          sshKeyPath,
			SSHCommand:       sshCmd,
			RemoteHost:       chain[len(chain)-1].HostName,
		}
		SaveState(state)
		SaveLastHost(strings.Join(chainNames, "|"))
		startPACServer()
		pacURL := fmt.Sprintf("http://127.0.0.1:%d/x_proxy.pac", Config.Network.PACHttpPort)
		setSystemProxy(pacURL)

		// Update state
		currentHost = chainStr
		isTunnelActive = true
		tunnelStartTime = time.Now()

		// Update menu
		updateMenuState()

		// Update tray icon and title
		updateTrayStatusOnline("Chain", chainStr)

		// Start monitoring for this connection
		go startMonitoring(&state)
	} else {
		logTunnelEvent("ERROR", chainStr, "Chain connection failed")
		// Reset icon to yellow
		updateTrayStatusReconnecting("Chain", chainStr)
	}
}
