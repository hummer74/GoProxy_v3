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
	"time"

	windows "golang.org/x/sys/windows"
)

// loadSSHKeyPassphrase loads SSH-KEY-PASS from file (var for test mocking)
var loadSSHKeyPassphrase = func() string {
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

// savePid saves process PID to file atomically (tmp+rename).
func savePid(file string, pid int, info string) {
	data, err := json.Marshal(PidData{Pid: pid, Info: info})
	if err != nil {
		log.Printf("Error marshalling PID data for file %s: %v", file, err)
		return
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("Error writing PID tmp file %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, file); err != nil {
		os.Remove(tmp)
		log.Printf("Error renaming PID file %s: %v", file, err)
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

// killStaleListeners kills processes LISTENING on the given TCP port,
// except our own process. Нужен на случай потерянных PID-файлов
// (stop без админа, килл трея): сирота ssh.exe держит :1080 и новый
// туннель биндится вторым — трафик расползается между ними.
func killStaleListeners(port int) {
	want := ":" + strconv.Itoa(port)
	cmd := exec.Command("netstat", "-ano", "-p", "TCP")
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return
	}
	self := os.Getpid()
	seen := make(map[int]bool)
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		// TCP <local> <remote> <state> <pid>
		if len(f) != 5 || f[0] != "TCP" || f[3] != "LISTENING" {
			continue
		}
		local := f[1]
		if idx := strings.LastIndex(local, ":"); idx < 0 || local[idx:] != want {
			continue
		}
		pid, err := strconv.Atoi(f[4])
		if err != nil || pid == 0 || pid == self || seen[pid] {
			continue
		}
		seen[pid] = true
		// Netstat-снапшот мог устареть (pre-flight уже вышел) — проверяем жив ли.
		if !checkPidAlive(pid) {
			continue
		}
		debugLog("UTILS", "Stale listener on %d: PID %d — killing", port, pid)
		logTunnelEvent("WARN", "startup", "Stale tunnel on port "+strconv.Itoa(port)+", killing")
		killPid(pid)
	}
}

// killPid attempts to terminate a process by PID.
// Возвращает true если процесс мёртв после вызова. Отказ (чужой elevation)
// теперь виден в логе, а не молча.
func killPid(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err == nil {
		_ = proc.Kill()
		_ = proc.Release()
	}

	// Fallback to taskkill
	cmd := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid))
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.CombinedOutput()
	if err != nil {
		// taskkill матерится и на уже вышедший PID (гонка со снапшотом
		// netstat): проверяем жив ли — мёртвый это успех, молчим в debug.
		// Живой после отказа = чужой elevation, вот это уже WARN.
		// (Текст ошибки не парсим: taskkill пишет в cp866, в логе кракозябры.)
		time.Sleep(300 * time.Millisecond)
		if !checkPidAlive(pid) {
			debugLog("UTILS", "killPid(%d): already exited, taskkill raced (%s)", pid, strings.TrimSpace(string(out)))
			return true
		}
		debugLog("UTILS", "killPid(%d) failed: %v — нужен админ?", pid, err)
		logTunnelEvent("WARN", "startup", "Cannot kill PID "+strconv.Itoa(pid)+" (run elevated?)")
		return false
	}

	// Проверяем, что процесс реально ушёл
	time.Sleep(300 * time.Millisecond)
	if checkPidAlive(pid) {
		debugLog("UTILS", "killPid(%d): still alive after kill", pid)
		return false
	}
	return true
}

// checkPidAlive проверяет существование процесса без убийства.
// Сравнивает ТОЧНОЕ поле PID в CSV tasklist: старый strings.Contains
// давал ложные "alive" (PID 123 "находился" в строке PID 1234), из-за чего
// killPid сыпал WARN "Cannot kill PID" по уже мёртвым probe-процессам.
func checkPidAlive(pid int) bool {
	cmd := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH")
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	out, _ := cmd.CombinedOutput()
	want := `"` + strconv.Itoa(pid) + `"`
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, ",")
		if len(fields) >= 2 && strings.TrimSpace(fields[1]) == want {
			return true
		}
	}
	return false
}
