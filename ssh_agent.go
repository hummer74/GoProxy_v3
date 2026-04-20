package main

import (
    "context"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "time"

    windows "golang.org/x/sys/windows"
)

// ensureSSHAgent checks if ssh-agent is running, starts it if needed, and adds the key
func ensureSSHAgent(sshKeyPath, sshKeyPass string) bool {
    debugLog("AGENT", "ensureSSHAgent: key=%s", sshKeyPath)
    if sshKeyPath == "" {
        return true
    }

    absKeyPath := resolveSSHKeyPath(Config.Paths.WorkDir, sshKeyPath)
    if _, err := os.Stat(absKeyPath); err != nil {
        logTunnelEvent("WARN", "SSH-Agent", fmt.Sprintf("Key file not found: %s", absKeyPath))
        return false
    }

    if !checkAndStartSSHAgent() {
        logTunnelEvent("WARN", "SSH-Agent", "Failed to ensure SSH-Agent service is running")
        return false
    }

    if isKeyInAgent(absKeyPath) {
        return true
    }

    if addKeyToAgent(absKeyPath, sshKeyPass) {
        return true
    }

    // Fallback: try using trySSHAdd logic with manual pass handing
    if trySSHAdd(absKeyPath, sshKeyPass) {
        return true
    }

    return false
}

// checkAndStartSSHAgent checks and starts SSH-Agent service if necessary
func checkAndStartSSHAgent() bool {
    debugLog("AGENT", "Checking SSH-Agent service...")
    if _, err := exec.LookPath("ssh-add"); err != nil {
        return false
    }

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    cmdCheck := exec.CommandContext(ctx, "sc", "query", "ssh-agent")
    cmdCheck.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
    output, _ := cmdCheck.CombinedOutput()

    if strings.Contains(string(output), "RUNNING") {
        return true
    }

    ctxStart, cancelStart := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancelStart()
    cmdStart := exec.CommandContext(ctxStart, "sc", "start", "ssh-agent")
    cmdStart.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
    _ = cmdStart.Run()

    time.Sleep(1 * time.Second)

    ctxCheck2, cancelCheck2 := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancelCheck2()
    cmdCheck2 := exec.CommandContext(ctxCheck2, "sc", "query", "ssh-agent")
    cmdCheck2.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
    output2, _ := cmdCheck2.CombinedOutput()

    result := strings.Contains(string(output2), "RUNNING")
    debugLog("AGENT", "SSH-Agent service check result: running=%v", result)
    return result
}

func isKeyInAgent(keyPath string) bool {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    cmd := exec.CommandContext(ctx, "ssh-add", "-l")
    cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
    output, err := cmd.CombinedOutput()

    if err != nil {
        return false
    }

    outStr := string(output)
    if strings.Contains(outStr, "The agent has no identities") {
        return false
    }

    return checkKeyFingerprint(keyPath, outStr)
}

func checkKeyFingerprint(keyPath, agentOutput string) bool {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    cmd := exec.CommandContext(ctx, "ssh-keygen", "-l", "-f", keyPath)
    cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
    fpOut, err := cmd.CombinedOutput()
    if err != nil {
        return false
    }
    parts := strings.Fields(string(fpOut))
    if len(parts) >= 2 {
        fingerprint := parts[1]
        return strings.Contains(agentOutput, fingerprint)
    }
    return false
}

func addKeyToAgent(keyPath, passphrase string) bool {
    debugLog("AGENT", "Adding key to agent: %s", filepath.Base(keyPath))
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    cmd := exec.CommandContext(ctx, "ssh-add", keyPath)
    cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}

    askPassScript := filepath.Join(Config.Paths.WorkDir, "askpass.bat")
    if passphrase != "" {
        _ = os.WriteFile(askPassScript, []byte(fmt.Sprintf("@echo %s\r\n", passphrase)), 0755)
    } else {
        _ = os.WriteFile(askPassScript, []byte("@echo.\r\n"), 0755)
    }
    defer os.Remove(askPassScript)

    cmd.Env = append(os.Environ(), "DISPLAY=dummy:0", "SSH_ASKPASS="+askPassScript)

    output, err := cmd.CombinedOutput()
    outputStr := string(output)

    if ctx.Err() == context.DeadlineExceeded {
        // Race condition: context expired but ssh-add may have succeeded.
        // Verify the key is actually in the agent before reporting failure.
        if isKeyInAgent(keyPath) {
            debugLog("AGENT", "ssh-add timed out but key is present in agent — success")
            return true
        }
        logTunnelEvent("ERROR", "SSH-Agent", "ssh-add timed out and key not in agent")
        return false
    }

    if err == nil {
        logTunnelEvent("OK", "SSH-Agent", fmt.Sprintf("Identity added: %s", filepath.Base(keyPath)))
        return true
    }

    if strings.Contains(outputStr, "already exists") || strings.Contains(outputStr, "Identity added") {
        return true
    }

    logTunnelEvent("WARN", "SSH-Agent", fmt.Sprintf("Failed to add key: %s", strings.TrimSpace(outputStr)))
    return false
}

// trySSHAdd attempts to add SSH-KEY using ssh-add (legacy support)
func trySSHAdd(sshKeyPath, sshKeyPass string) bool {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    cmd := exec.CommandContext(ctx, "ssh-add", sshKeyPath)

    if sshKeyPass != "" {
        tempDir := os.TempDir()
        batPath := filepath.Join(tempDir, fmt.Sprintf("ssh_pass_%d.bat", os.Getpid()))
        script := fmt.Sprintf("@echo %s", sshKeyPass)
        if err := os.WriteFile(batPath, []byte(script), 0600); err != nil {
            return false
        }
        defer os.Remove(batPath)

        cmd.Env = append(os.Environ(),
            fmt.Sprintf("SSH_ASKPASS=%s", batPath),
            "SSH_ASKPASS_REQUIRE=force",
            "DISPLAY=dummy:0",
        )
    }

    cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
    output, err := cmd.CombinedOutput()
    outputStr := string(output)

    if ctx.Err() == context.DeadlineExceeded {
        if isKeyInAgent(sshKeyPath) {
            debugLog("AGENT", "trySSHAdd timed out but key is present in agent — success")
            return true
        }
        logTunnelEvent("ERROR", "SSH-Agent", "trySSHAdd timed out and key not in agent")
        return false
    }

    if err == nil {
        return true
    }

    if strings.Contains(outputStr, "already exists") || strings.Contains(outputStr, "Identity added") {
        return true
    }

    return false
}

// stopSSHAgent stops the ssh-agent service if it is running
func stopSSHAgent() bool {
    cmdStop := exec.Command("sc", "stop", "ssh-agent")
    cmdStop.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
    if err := cmdStop.Run(); err != nil {
        return false
    }
    return true
}

// cleanupSSHAgent removes added keys from agent
func cleanupSSHAgent() {
    debugLog("AGENT", "Cleaning up SSH agent")
    cmd := exec.Command("ssh-add", "-D")
    cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
    _ = cmd.Run()
    _ = stopSSHAgent()
}

// testSSHKeyDirectly tests if SSH-KEY works by trying a simple SSH command.
// It first checks if the key is loaded in ssh-agent, then attempts a connection test.
// Returns true if the key is valid and functional, false otherwise.
func testSSHKeyDirectly(sshKeyPath string) bool {
    debugLog("AGENT", "Testing SSH key directly: %s", filepath.Base(sshKeyPath))

    // Step 1: Check if any keys are loaded in ssh-agent
    ctxList, cancelList := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancelList()
    cmdList := exec.CommandContext(ctxList, "ssh-add", "-l")
    cmdList.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
    output, err := cmdList.CombinedOutput()
    if err == nil && len(output) > 0 {
        // Verify the specific key is in agent by comparing fingerprints
        if isKeyInAgent(sshKeyPath) {
            debugLog("AGENT", "SSH key found in agent")
            return true
        }
        debugLog("AGENT", "SSH key not found in agent")
    }

    // Step 2: Attempt direct SSH connection test using the key.
    // BatchMode=yes prevents passphrase prompts, ConnectTimeout limits wait time.
    // A network/connection error means the key was accepted (auth failed but key valid).
    // Only auth-related errors indicate key is invalid.
    ctxTest, cancelTest := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancelTest()
    testCmd := exec.CommandContext(ctxTest, "ssh",
        "-o", "BatchMode=yes",
        "-o", "ConnectTimeout=1",
        "-o", "StrictHostKeyChecking=no", // Avoid host key verification issues during test
        "-i", sshKeyPath,
        "localhost", "echo test")
    testCmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}

    _, err = testCmd.CombinedOutput()
    if err != nil {
        // Check for auth-related errors that indicate key is invalid
        exitErr, ok := err.(*exec.ExitError)
        if !ok {
            // Non-standard error (e.g., context timeout) — treat as failure
            debugLog("AGENT", "SSH test failed with unexpected error: %v", err)
            return false
        }

        exitMsg := string(exitErr.Stderr)
        // Permission denied or key-related errors mean the key is invalid
        if strings.Contains(exitMsg, "Permission denied") ||
            strings.Contains(exitMsg, "private key") ||
            strings.Contains(exitMsg, "Authentication failed") {
            debugLog("AGENT", "SSH test failed: auth error indicates invalid key")
            return false
        }

        // Connection/network errors mean key was accepted but connection failed — key is valid
        debugLog("AGENT", "SSH test: key accepted (connection error is expected)")
        return true
    }

    // SSH command succeeded — key works perfectly
    debugLog("AGENT", "SSH test: key works successfully")
    return true
}
