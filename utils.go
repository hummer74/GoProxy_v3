// utils.go
package main

import (
	"encoding/json"
	"log"
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
		log.Printf("Error marshalling PID data for file %s: %v", file, err)
		return
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		log.Printf("Error writing PID file %s: %v", file, err)
	}
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
	log.Printf("Attempting to kill process '%s' using PID file: %s", name, pidFile)
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}

	var pid int = -1 // Initialize with an invalid PID
	var pidData PidData

	// Attempt JSON unmarshalling first
	if json.Unmarshal(data, &pidData) == nil {
		pid = pidData.Pid
	} else {
		// Fallback for old simple PID file format
		if pidFallback, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			pid = pidFallback
		} else {
			log.Printf("Warning: Failed to parse PID from %s using fallback method (not JSON): %v", pidFile, err)
		}
	}

	// Attempt to kill the process if a valid PID was found
	if pid != -1 {
		killPid(pid) // Assuming killPid handles its own error logging internally or we accept silent failure on Kill()
	} else {
		log.Printf("Warning: Could not determine PID from %s using either JSON or fallback parsing.", pidFile)
	}

	// Attempt to remove the PID file and log result
	if err := os.Remove(pidFile); err != nil {
		log.Printf("Error removing PID file %s after attempting to kill process: %v", pidFile, err)
	} else {
		log.Printf("Successfully removed PID file: %s", pidFile)
	}
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
