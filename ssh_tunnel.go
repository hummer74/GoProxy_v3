// ssh_tunnel.go
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	windows "golang.org/x/sys/windows"
)

// tunnelGeneration is incremented every time a new connection is initiated
// or an existing tunnel is killed. Long-running retry loops check this value
// before each retry to detect cancellation.
var tunnelGeneration uint64

// nextTunnelGeneration atomically increments and returns the new generation.
func nextTunnelGeneration() uint64 {
	return atomic.AddUint64(&tunnelGeneration, 1)
}

// currentTunnelGeneration returns the current generation (for comparison).
func currentTunnelGeneration() uint64 {
	return atomic.LoadUint64(&tunnelGeneration)
}

// testTunnelConnectivity starts a temporary SSH tunnel on testPort (NOT the
// production port) to verify that a full SOCKS5 tunnel can be established.
// It NEVER touches the main PID file or the running production tunnel.
//
// Flow:
//  1. Clone the SSH command with a different -D port (testPort).
//  2. Start the SSH process in the background.
//  3. Poll checkProxyConnectivityOnPort(testPort) every 500 ms.
//  4. On SOCKS5 success  → kill test process, return true.
//  5. On timeout / error → kill test process, return false.
//
// The caller should call this BEFORE establishConnection to guarantee that
// killing the old production tunnel is safe.
func testTunnelConnectivity(sshCmd []string, testPort int, timeout time.Duration) bool {
	myGen := currentTunnelGeneration()
	testCmd := replaceSSHTunnelPort(sshCmd, testPort)
	host := extractHostFromSSHCommand(testCmd)

	debugLog("TUNNEL", "TEST start: host=%s testPort=%d timeout=%v gen=%d", host, testPort, timeout, myGen)

	cmd := exec.Command(testCmd[0], testCmd[1:]...)
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		debugLog("TUNNEL", "TEST: failed to start SSH process: %v", err)
		return false
	}

	testPid := cmd.Process.Pid
	// Always clean up the test process.
	defer func() {
		if cmd.Process != nil {
			killPid(testPid)
			debugLog("TUNNEL", "TEST: cleaned up PID %d", testPid)
		}
	}()

	// Wait goroutine — detects early process exit.
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("panic: %v", r)
			}
		}()
		done <- cmd.Wait()
	}()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	timeoutCh := time.After(timeout)

	for {
		select {
		case err := <-done:
			stderrStr := stderr.String()
			debugLog("TUNNEL", "TEST: process exited (err=%v, stderr=%q)", err, stderrStr)
			return false

		case <-timeoutCh:
			stderrStr := stderr.String()
			if stderrStr != "" {
				debugLog("TUNNEL", "TEST: timeout after %v, stderr=%q", timeout, stderrStr)
			} else {
				debugLog("TUNNEL", "TEST: timeout after %v (no stderr)", timeout)
			}
			return false

		case <-ticker.C:
			// Check if this test has been superseded by a new connection
			if currentTunnelGeneration() != myGen {
				debugLog("TUNNEL", "TEST: generation changed (%d → %d), aborting test", myGen, currentTunnelGeneration())
				return false
			}

			if checkProxyConnectivityOnPort(testPort) {
				debugLog("TUNNEL", "TEST: SOCKS5 confirmed on port %d — host %s is viable", testPort, host)
				return true
			}

			// Detect terminal errors early.
			stderrStr := stderr.String()
			if stderrStr != "" {
				low := strings.ToLower(stderrStr)
				if strings.Contains(low, "permission denied") ||
					strings.Contains(low, "no route to host") ||
					strings.Contains(low, "connection reset") ||
					strings.Contains(low, "exitonforwardfailure") {
					debugLog("TUNNEL", "TEST: terminal error on %s: %s", host, stderrStr)
					return false
				}
			}
		}
	}
}

// startSSHTunnel starts the SSH tunnel with default retries (chain length 1)
func startSSHTunnel(sshCmd []string) bool {
	return startSSHTunnelWithRetries(sshCmd, 1)
}

// startSSHTunnelWithRetries attempts to start SSH tunnel with progressive timeouts based on chain length.
// Checks tunnelGeneration before each retry — if a new connection was initiated or the
// tunnel was killed, the retry loop is aborted immediately.
func startSSHTunnelWithRetries(sshCmd []string, chainLength int) bool {
	myGen := currentTunnelGeneration()
	debugLog("TUNNEL", "Starting tunnel with %d retries, chain length %d, gen=%d", 3, chainLength, myGen)

	// Base timeout: 15 seconds per hop, but at least 15s and at most 90s
	baseTimeout := 15 * time.Duration(chainLength) * time.Second
	if baseTimeout < 15*time.Second {
		baseTimeout = 15*time.Second
	}
	if baseTimeout > 90*time.Second {
		baseTimeout = 90*time.Second
	}

	// Progressive timeouts: first = base, second = base*1.5, third = base*2
	timeouts := []time.Duration{
		baseTimeout,
		baseTimeout * 3 / 2,
		baseTimeout * 2,
	}

	for attempt := 1; attempt <= 3; attempt++ {
		// Check if this connection attempt has been superseded
		if currentTunnelGeneration() != myGen {
			debugLog("TUNNEL", "Generation changed (%d → %d), aborting retries", myGen, currentTunnelGeneration())
			return false
		}

		timeout := timeouts[attempt-1]
		debugLog("TUNNEL", "Attempt %d/%d (timeout %v, gen=%d)", attempt, 3, timeout, myGen)

		if startSSHTunnelWithTimeout(sshCmd, timeout, myGen) {
			debugLog("TUNNEL", "Tunnel established on attempt %d", attempt)
			logTunnelEvent("OK", extractHostFromSSHCommand(sshCmd),
				fmt.Sprintf("SSH tunnel established on attempt %d/%d (%d-hop chain)", attempt, 3, chainLength))
			return true
		}

		// Re-check generation after timeout (process was killed or timed out)
		if currentTunnelGeneration() != myGen {
			debugLog("TUNNEL", "Generation changed after attempt %d, aborting retries", attempt)
			return false
		}

		if attempt < 3 {
			logTunnelEvent("WARN", extractHostFromSSHCommand(sshCmd),
				fmt.Sprintf("Attempt %d failed, retrying in 2 seconds...", attempt))
			time.Sleep(2 * time.Second)
		}
	}

	debugLog("TUNNEL", "All attempts failed")
	logTunnelEvent("ERROR", extractHostFromSSHCommand(sshCmd),
		fmt.Sprintf("Failed to establish SSH tunnel after 3 attempts (%d-hop chain)", chainLength))
	return false
}

// extractHostFromSSHCommand extracts full chain description from SSH command for logging
func extractHostFromSSHCommand(sshCmd []string) string {
	var jumps []string
	finalHost := ""
	finalPort := ""
	finalUser := ""

	// Parse -J option
	for i, arg := range sshCmd {
		if arg == "-J" && i+1 < len(sshCmd) {
			jumps = strings.Split(sshCmd[i+1], ",")
			break
		}
	}

	// Parse final host, port, user
	// The final host is usually the last argument that is not an option
	for i := len(sshCmd) - 1; i >= 0; i-- {
		arg := sshCmd[i]
		if !strings.HasPrefix(arg, "-") && arg != "-l" && arg != "-p" && arg != "-i" {
			// This is likely the final host
			if strings.Contains(arg, "@") {
				parts := strings.SplitN(arg, "@", 2)
				finalUser = parts[0]
				hostPort := parts[1]
				if strings.Contains(hostPort, ":") {
					hp := strings.SplitN(hostPort, ":", 2)
					finalHost = hp[0]
					finalPort = hp[1]
				} else {
					finalHost = hostPort
				}
			} else {
				finalHost = arg
			}
			break
		}
	}

	// If final user not found, look for -l option
	if finalUser == "" {
		for i, arg := range sshCmd {
			if arg == "-l" && i+1 < len(sshCmd) {
				finalUser = sshCmd[i+1]
				break
			}
		}
	}

	// If final port not found, look for -p option
	if finalPort == "" {
		for i, arg := range sshCmd {
			if arg == "-p" && i+1 < len(sshCmd) {
				finalPort = sshCmd[i+1]
				break
			}
		}
	}

	// Build description
	var parts []string
	for _, jump := range jumps {
		parts = append(parts, jump)
	}
	if finalHost != "" {
		userPart := ""
		if finalUser != "" {
			userPart = finalUser + "@"
		}
		portPart := ""
		if finalPort != "" && finalPort != "22" {
			portPart = ":" + finalPort
		}
		parts = append(parts, userPart+finalHost+portPart)
	}

	if len(parts) == 0 {
		return "unknown"
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts, " -> ")
}

// startSSHTunnelWithTimeout runs SSH command and checks if tunnel becomes available.
// Accepts a gen parameter to detect cancellation by newer connection attempts.
func startSSHTunnelWithTimeout(sshCmd []string, timeout time.Duration, gen uint64) bool {
	host := extractHostFromSSHCommand(sshCmd)

	cmd := exec.Command(sshCmd[0], sshCmd[1:]...)
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}

	// Capture both stdout and stderr for debugging
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Start the process
	if err := cmd.Start(); err != nil {
		logTunnelEvent("ERROR", host, fmt.Sprintf("Failed to start SSH process: %v", err))
		return false
	}

	// Save PID immediately
	savePid(Config.TempFiles.SSHTunnelPID, cmd.Process.Pid, "SSH Tunnel")
	debugLog("TUNNEL", "SSH process started (PID=%d, gen=%d), waiting for connectivity (timeout %v)", cmd.Process.Pid, gen, timeout)

	// Channel to track if process exits early
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				debugLog("TUNNEL", "PANIC in SSH Wait goroutine: %v", r)
				writeCrashLog(r)
				done <- fmt.Errorf("panic in SSH wait: %v", r)
			}
		}()
		done <- cmd.Wait()
	}()

	// Use a ticker for periodic connectivity checks instead of busy-wait select/default
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	// Single-shot timer for the overall timeout
	timeoutCh := time.After(timeout)

	for {
		select {
		case err := <-done:
			// Process exited
			// Check if killed by a newer generation
			if currentTunnelGeneration() != gen {
				debugLog("TUNNEL", "SSH process killed by generation change, aborting")
				os.Remove(Config.TempFiles.SSHTunnelPID)
				return false
			}

			stderrOutput := stderr.String()

			if err != nil {
				logTunnelEvent("ERROR", host, fmt.Sprintf("SSH process exited with error: %v", err))
				if stderrOutput != "" {
					logTunnelEvent("ERROR", host, "SSH error: "+stderrOutput)
				}
			} else {
				logTunnelEvent("WARN", host, "SSH process exited normally (should run in background)")
			}

			os.Remove(Config.TempFiles.SSHTunnelPID)
			return false

		case <-timeoutCh:
			// Check if superseded before logging timeout
			if currentTunnelGeneration() != gen {
				debugLog("TUNNEL", "Timeout superseded by generation change, aborting")
				if cmd.Process != nil {
					killPid(cmd.Process.Pid)
				}
				os.Remove(Config.TempFiles.SSHTunnelPID)
				return false
			}

			// Overall timeout exceeded
			stderrOutput := stderr.String()
			logTunnelEvent("WARN", host, fmt.Sprintf("SSH tunnel timeout reached after %v", timeout))

			if stderrOutput != "" {
				logTunnelEvent("ERROR", host, "SSH error: "+stderrOutput)
			}

			if cmd.Process != nil {
				killPid(cmd.Process.Pid)
			}
			os.Remove(Config.TempFiles.SSHTunnelPID)
			return false

		case <-ticker.C:
			// Check if superseded
			if currentTunnelGeneration() != gen {
				debugLog("TUNNEL", "Generation changed during poll, aborting tunnel start")
				if cmd.Process != nil {
					killPid(cmd.Process.Pid)
				}
				os.Remove(Config.TempFiles.SSHTunnelPID)
				return false
			}

			// Periodic connectivity check
			if checkProxyConnectivity() {
				return true
			}

			stderrOutput := stderr.String()
			if stderrOutput != "" {
				if strings.Contains(stderrOutput, "Permission denied") || strings.Contains(stderrOutput, "No route to host") || strings.Contains(stderrOutput, "Connection reset") {
					logTunnelEvent("ERROR", host, "Detected terminal SSH error during tunnel startup: "+stderrOutput)
					if cmd.Process != nil {
						killPid(cmd.Process.Pid)
					}
					os.Remove(Config.TempFiles.SSHTunnelPID)
					return false
				}
			}
		}
	}
}
