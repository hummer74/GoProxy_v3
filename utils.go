// utils.go
package main

import (
        "encoding/json"
        "os"
        "os/exec"
        "path/filepath"
        "strconv"
        "strings"

        windows "golang.org/x/sys/windows"
)

// loadSSHKeyPassphrase loads SSH-KEY-PASS from file
func loadSSHKeyPassphrase() string {
        if Config.Paths.SSHKeyPassword == "" {
                return ""
        }

        data, err := os.ReadFile(Config.Paths.SSHKeyPassword)
        if err != nil {
                return ""
        }

        debugLog("UTILS", "SSH key passphrase loaded")
        return strings.TrimSpace(string(data))
}

// resolveSSHKeyPath resolves SSH-KEY path (relative or absolute)
func resolveSSHKeyPath(workDir, keyPath string) string {
        if keyPath == "" {
                return ""
        }
        if filepath.IsAbs(keyPath) {
                return keyPath
        }
        std := filepath.Join(workDir, ".ssh", keyPath)
        if _, err := os.Stat(std); err == nil {
                return std
        }
        return filepath.Join(workDir, keyPath)
}

// savePid saves process PID to file
func savePid(file string, pid int, info string) {
        data, err := json.Marshal(PidData{Pid: pid, Info: info})
        if err != nil {
                return
        }
        _ = os.WriteFile(file, data, 0644)
}

// findHostByName finds a host by name in the hosts list
func findHostByName(hosts []HostConfig, name string) *HostConfig {
        for i := range hosts {
                if hosts[i].Name == name {
                        return &hosts[i]
                }
        }
        return nil
}

// checkProcessRunning checks if a process is running by its PID file
func checkProcessRunning(pidFile string) bool {
        data, err := os.ReadFile(pidFile)
        if err != nil {
                return false
        }

        var pidData PidData
        if err := json.Unmarshal(data, &pidData); err != nil {
                return false
        }

        // Check if process exists
        proc, err := os.FindProcess(pidData.Pid)
        if err != nil {
                return false
        }

        // Try to signal the process
        err = proc.Signal(windows.Signal(0))
        return err == nil
}

// killProcessByFile kills a process by its PID file
func killProcessByFile(pidFile string, name string) {
        data, err := os.ReadFile(pidFile)
        if err != nil {
                return
        }

        var pidData PidData
        if json.Unmarshal(data, &pidData) == nil {
                killPid(pidData.Pid)
        } else {
                // Fallback for old simple PID file format
                if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
                        killPid(pid)
                }
        }
        os.Remove(pidFile)
}

// killPid attempts to terminate a process by PID
func killPid(pid int) {
        proc, err := os.FindProcess(pid)
        if err == nil {
                _ = proc.Kill()
                _ = proc.Release()
        }

        // Fallback to taskkill
        cmd := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid))
        cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
        _, _ = cmd.CombinedOutput()
}
