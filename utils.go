// utils.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
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

// pidFromFile reads a PID plus its identity tag without killing anything.
// ok=false when the file is missing, unparsable, or holds an invalid PID.
func pidFromFile(pidFile string) (pid int, info string, hasInfo bool, ok bool) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, "", false, false
	}
	var pidData PidData
	if json.Unmarshal(data, &pidData) == nil && pidData.Pid > 0 {
		return pidData.Pid, pidData.Info, pidData.Info != "", true
	}
	if n, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && n > 0 {
		return n, "", false, true
	}
	return 0, "", false, false
}

// killProcessByFile kills a process by its PID file.
// SAFETY: never kills blindly by PID — the PID file may be stale and the
// PID reused by a foreign process. The expected image comes from the PID
// file itself (PidData.Info): "SSH Tunnel*" → ssh.exe, everything else
// (tray, PAC server) → our own executable. Mismatch (or PID 0/4/System) →
// stale file removed, process left alone.
func killProcessByFile(pidFile string, name string) {
	log.Printf("Attempting to kill process '%s' using PID file: %s", name, pidFile)
	pid, info, hasInfo, ok := pidFromFile(pidFile)
	if !ok {
		log.Printf("Warning: Could not determine PID from %s (missing or corrupt file).", pidFile)
		return
	}

	// Attempt to kill the process if a valid PID was found
	if !verifyPidForKill(pid, info, hasInfo) {
		// Stale PID file pointing at a foreign process:
		// drop the file, keep hands off the process.
		os.Remove(pidFile)
		return
	}
	// Attempt to kill the process after verification.
	killPid(pid)

	// Attempt to remove the PID file and log result
	if err := os.Remove(pidFile); err != nil {
		log.Printf("Error removing PID file %s after attempting to kill process: %v", pidFile, err)
	} else {
		log.Printf("Successfully removed PID file: %s", pidFile)
	}
}

// verifyPidForKill decides whether pid may be terminated for the given PID
// file identity. info is the PidData.Info string ("" when unknown/legacy).
// Returns false for system PIDs and for image mismatches.
func verifyPidForKill(pid int, info string, hasInfo bool) bool {
	if pid <= 4 {
		log.Printf("Refusing to kill system PID %d (stale PID file?)", pid)
		return false
	}
	var expect string
	if hasInfo {
		expect = expectedImageFor(info)
	} else {
		// Legacy plain-PID file: only our own kinds of processes ever
		// wrote these (ssh tunnel, tray). Refuse anything else.
		img, ok := processImageName(pid)
		if !ok {
			return true // process already gone — killPid is a harmless no-op
		}
		if !isOwnOrSSH(img) {
			log.Printf("Refusing to kill PID %d (%s): not ours, stale PID file?", pid, img)
			return false
		}
		return true
	}
	if !verifyPidImage(pid, expect) {
		return false
	}
	// ssh.exe needs one more proof: our tunnels always carry "-D".
	// A foreign ssh (interactive session, other tool) must survive.
	if strings.EqualFold(expect, "ssh.exe") {
		if cmdline, ok := processCommandLine(pid); ok && !strings.Contains(cmdline, "-D") {
			log.Printf("Refusing to kill PID %d: ssh.exe without -D in cmdline", pid)
			return false
		}
	}
	return true
}

// expectedImageFor maps a PID file's Info tag to the executable we accept.
func expectedImageFor(info string) string {
	if strings.Contains(strings.ToLower(info), "ssh") {
		return "ssh.exe"
	}
	return ownExeName()
}

// ownExeName returns the lower-cased file name of this executable.
func ownExeName() string {
	if exe, err := os.Executable(); err == nil {
		return strings.ToLower(filepath.Base(exe))
	}
	return "goproxy.exe"
}

// isOwnOrSSH reports whether image is our executable or ssh.exe.
func isOwnOrSSH(img string) bool {
	img = strings.ToLower(img)
	return img == "ssh.exe" || img == ownExeName()
}

// verifyPidImage checks the running image name matches the expectation.
// Unknown PID (already exited) returns false — the caller treats that as
// "nothing to kill".
func verifyPidImage(pid int, expect string) bool {
	img, ok := processImageName(pid)
	if !ok {
		return false
	}
	if !strings.EqualFold(img, expect) {
		log.Printf("Refusing to kill PID %d: image is %q, expected %q (stale PID file?)", pid, img, expect)
		return false
	}
	return true
}

// processImageName returns the executable name (e.g. "ssh.exe") of a PID.
// ok=false when the process does not exist or cannot be queried.
func processImageName(pid int) (string, bool) {
	cmd := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH")
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, ",")
		if len(fields) >= 2 && strings.TrimSpace(fields[1]) == `"`+strconv.Itoa(pid)+`"` {
			return strings.Trim(strings.TrimSpace(fields[0]), `"`), true
		}
	}
	return "", false
}

// processCommandLine returns the full command line of a PID via CIM.
// ok=false when it cannot be determined (no rights, no PowerShell, gone).
func processCommandLine(pid int) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command",
		"(Get-CimInstance Win32_Process -Filter 'ProcessId="+strconv.Itoa(pid)+"').CommandLine")
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// killStaleListeners kills OUR OWNED processes LISTENING on the given TCP port,
// except our own process. Нужен на случай потерянных PID-файлов
// (stop без админа, килл трея): сирота ssh.exe держит :1080 и новый
// туннель биндится вторым — трафик расползается между ними.
//
// SAFETY: kills only ssh.exe whose command line contains "-D" (our tunnel
// shape). A foreign listener (other VPN/proxy/clash) is NEVER touched —
// only reported. Unverifiable owners are skipped, not killed.
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
		// Owner check: только наш сирота ssh.exe с "-D" в командной строке.
		// Чужой слушатель (другой VPN/прокси) — не трогаем, только репортим.
		img, imgOk := processImageName(pid)
		if !imgOk || !strings.EqualFold(img, "ssh.exe") {
			debugLog("UTILS", "Listener on %d: PID %d is %q, not our ssh.exe — leaving alone", port, pid, img)
			logTunnelEvent("WARN", "startup", fmt.Sprintf("Port %d held by foreign %s (PID %d) — not touching", port, img, pid))
			continue
		}
		if cmdline, cmdOk := processCommandLine(pid); !cmdOk || !strings.Contains(cmdline, "-D") {
			debugLog("UTILS", "Listener on %d: ssh.exe PID %d without -D in cmdline — leaving alone", port, pid)
			logTunnelEvent("WARN", "startup", fmt.Sprintf("Port %d held by non-tunnel ssh (PID %d) — not touching", port, pid))
			continue
		}
		debugLog("UTILS", "Stale listener on %d: orphan ssh.exe PID %d — killing", port, pid)
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
