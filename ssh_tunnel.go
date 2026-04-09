// ssh_tunnel.go
package main

import (
        "bytes"
        "fmt"
        "os"
        "os/exec"
        "strings"
        "time"

        windows "golang.org/x/sys/windows"
)

// startSSHTunnel starts the SSH tunnel with default retries (chain length 1)
func startSSHTunnel(sshCmd []string) bool {
        return startSSHTunnelWithRetries(sshCmd, 1)
}

// startSSHTunnelWithRetries attempts to start SSH tunnel with progressive timeouts based on chain length
func startSSHTunnelWithRetries(sshCmd []string, chainLength int) bool {
        debugLog("TUNNEL", "Starting tunnel with %d retries, chain length %d", 3, chainLength)

        // Base timeout: 15 seconds per hop, but at least 15s and at most 90s
        baseTimeout := 15 * time.Duration(chainLength) * time.Second
        if baseTimeout < 15*time.Second {
                baseTimeout = 15 * time.Second
        }
        if baseTimeout > 90*time.Second {
                baseTimeout = 90 * time.Second
        }

        // Progressive timeouts: first = base, second = base*1.5, third = base*2
        timeouts := []time.Duration{
                baseTimeout,
                baseTimeout * 3 / 2,
                baseTimeout * 2,
        }

        for attempt := 1; attempt <= 3; attempt++ {
                timeout := timeouts[attempt-1]
                debugLog("TUNNEL", "Attempt %d/%d (timeout %v)", attempt, 3, timeout)

                if startSSHTunnelWithTimeout(sshCmd, timeout) {
                        debugLog("TUNNEL", "Tunnel established on attempt %d", attempt)
                        logTunnelEvent("OK", extractHostFromSSHCommand(sshCmd),
                                fmt.Sprintf("SSH tunnel established on attempt %d/%d (%d-hop chain)", attempt, 3, chainLength))
                        return true
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

// startSSHTunnelWithTimeout runs SSH command and checks if tunnel becomes available
func startSSHTunnelWithTimeout(sshCmd []string, timeout time.Duration) bool {
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
        debugLog("TUNNEL", "SSH process started (PID saved), waiting for connectivity (timeout %v)", timeout)

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
