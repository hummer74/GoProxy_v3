// main.go
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	windows "golang.org/x/sys/windows"
)

var appMutex windows.Handle // Global mutex handle

// createAppMutex creates named mutex to prevent multiple instances
func createAppMutex() (windows.Handle, error) {
	execPath, _ := os.Executable()
	mutexName := fmt.Sprintf("Global\\GoProxyMutex_%x", filepath.Base(execPath))

	mutex, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(mutexName))
	if err != nil {
		return 0, fmt.Errorf("application is already running")
	}

	appMutex = mutex
	return mutex, nil
}

// ReleaseAppMutex releases the global application mutex
func ReleaseAppMutex() {
	if appMutex != 0 {
		windows.CloseHandle(appMutex)
		appMutex = 0
	}
}

// isElevated reports whether the current process token is elevated.
// On check failure it returns false (fail-open: caller continues unelevated).
func isElevated() bool {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return false
	}
	defer token.Close()
	return token.IsElevated()
}

// shellExecuteRunAs relaunches path with admin rights via ShellExecute "runas".
// args is forwarded verbatim (already quoted). Returns nil when the elevated
// child was started; a refusal (UAC "No") or failure returns an error.
func shellExecuteRunAs(path, args string) error {
	shell32 := windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteW := shell32.NewProc("ShellExecuteW")

	op, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(path)
	var params *uint16
	if args != "" {
		params, _ = windows.UTF16PtrFromString(args)
	}

	ret, _, _ := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(op)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		0,
		uintptr(windows.SW_SHOWNORMAL),
	)
	if ret <= 32 {
		return fmt.Errorf("ShellExecute runas failed (code %d)", ret)
	}
	return nil
}

// quoteArg quotes a single CLI argument for ShellExecute forwarding.
func quoteArg(a string) string {
	if strings.ContainsAny(a, " \t\"") {
		return `"` + strings.ReplaceAll(a, `"`, `""`) + `"`
	}
	return a
}

// ensureElevated relaunches the executable with admin rights when the current
// token is not elevated. The elevated child takes over; the parent exits.
// Already-elevated runs (scheduler HighestAvailable, elevated console/bat)
// are a no-op. UAC refusal or relaunch failure falls through to best-effort
// unelevated execution — never a loop, never a hard exit.
//
// NOTE: never call from tests — it exits the process on success.
func ensureElevated() {
	if isElevated() {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Println("Cannot determine executable path for elevation:", err)
		return
	}
	quoted := make([]string, 0, len(os.Args)-1)
	for _, a := range os.Args[1:] {
		quoted = append(quoted, quoteArg(a))
	}
	args := strings.Join(quoted, " ")
	debugLog("MAIN", "Not elevated — relaunching with admin rights (args: %q)", args)
	fmt.Println("Administrator rights required — requesting elevation...")
	if err := shellExecuteRunAs(exe, args); err != nil {
		fmt.Printf("Elevation declined (%v) — continuing without admin rights.\n", err)
		debugLog("MAIN", "Elevation declined/failed: %v — continuing unelevated", err)
		return
	}
	os.Exit(0) // elevated child takes over
}

func main() {
	// ── Global panic recovery — catches ANY unhandled panic ──
	defer func() {
		if r := recover(); r != nil {
			writeCrashLog(r)
			fmt.Fprintf(os.Stderr, "\n*** GoProxy CRASH ***\n")
			fmt.Fprintf(os.Stderr, "Panic: %v\n", r)
			fmt.Fprintf(os.Stderr, "Crash log: x_goproxy_crash.log\n")
			if debugEnabled {
				fmt.Fprintf(os.Stderr, "Debug log: x_goproxy_debug.log\n")
			}
			fmt.Fprintf(os.Stderr, "Press any key to exit...\n")
			time.Sleep(10 * time.Second)
			os.Exit(1)
		}
	}()

	// ── Self-elevation: stale elevated processes and the agent service
	// need admin to manage. Scheduler HighestAvailable / elevated console
	// are no-ops here; UAC refusal falls through to best-effort.
	ensureElevated()

	// ── Parse command line flags ──
	logFlag := flag.Bool("log", false, "Enable debug logging (x_goproxy_debug.log)")
	stopFlag := flag.Bool("stop", false, "Stop proxy and cleanup")
	diagFlag := flag.Bool("diag", false, "Collect diagnostics to x_diag.txt and exit")
	flag.Parse()

	if *diagFlag {
		runDiagMode()
		return
	}

	if *logFlag {
		debugEnabled = true
		fmt.Println("=== GoProxy Debug Mode ===")
		fmt.Println("Logging to x_goproxy_debug.log")
		fmt.Println("Crash logs to x_goproxy_crash.log")
		fmt.Println("=========================")
	}

	// ── Load configuration (нужен и stop-режиму для путей) ──
	execPath, _ := os.Executable()
	workDir := filepath.Dir(execPath)
	cfgPath := filepath.Join(workDir, "GoProxy.ini")

	if err := LoadConfig(cfgPath); err != nil {
		fmt.Printf("Warning: Could not load config file: %v\n", err)
		fmt.Println("Using default configuration...")
	}

	// ── Initialize debug logging AFTER config is loaded ──
	if *logFlag {
		initDebugLogging(Config.Paths.WorkDir)
	}

	// ── Stop mode НЕ берёт мьютекс: его задача — убить висящий инстанс.
	// (Раньше -stop упирался в "already running" при живом трее.)
	if *stopFlag {
		isTrayMode = true
		debugLog("MAIN", "Mode: STOP (no mutex)")
		runStopMode()
		return
	}

	// ── Create mutex for blocking multiple instances (только tray) ──
	_, err := createAppMutex()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		fmt.Println("GoProxy is already running in another instance.")
		fmt.Println("If this is incorrect, please wait a moment and try again.")
		time.Sleep(3 * time.Second)
		os.Exit(1)
	}
	defer ReleaseAppMutex()

	// ── Always tray mode ──
	isTrayMode = true

	debugLog("MAIN", "Executable: %s", execPath)
	debugLog("MAIN", "WorkDir: %s", workDir)
	debugLog("MAIN", "Config path: %s", cfgPath)

	debugLog("MAIN", "GoProxy starting (PID %d)", os.Getpid())
	debugLog("MAIN", "debugEnabled=%v, stopFlag=%v", *logFlag, *stopFlag)
	debugLog("MAIN", "Config: WorkDir=%s ProxyPort=%d PACPort=%d",
		Config.Paths.WorkDir, Config.Network.ProxyPort, Config.Network.PACHttpPort)

	// ── Sweep leftovers from a crashed/unclean previous session ──
	// (job of the old GoP_Start.bat, now in-app). Runs only after acquiring
	// the mutex, so no live instance exists and everything found is orphaned.
	runStartupCleanup()

	// ── Clean up stale flag files ──
	cleanupFlags()

	// ── Check and start SSH-Agent (stop mode сюда не доходит) ──
	debugLog("MAIN", "Checking SSH-Agent service...")
	if !checkAndStartSSHAgent() {
		debugLog("MAIN", "SSH-Agent check failed")
	} else {
		debugLog("MAIN", "SSH-Agent service is running")
	}
	defer cleanupSSHAgent()

	// ── Load priority host from x_lasthost.cfg (if available) ──
	debugLog("MAIN", "Loading priority host from x_lasthost.cfg...")
	loadedHost := LoadPriorityHost()
	if loadedHost != "" {
		priorityHost = loadedHost
		hasPriorityHost = true
		debugLog("MAIN", "Priority host loaded: %s", priorityHost)
	} else {
		debugLog("MAIN", "No priority host found in x_lasthost.cfg")
	}

	// ── Tray mode (stop вышел раньше без мьютекса) ──
	debugLog("MAIN", "Mode: TRAY")
	runTrayMode()
}

// runStartupCleanup performs best-effort cleanup of leftovers from a
// crashed or unclean previous session (boot with dead tunnel, killed tray).
// Must run only after acquiring the app mutex: then no live instance exists.
func runStartupCleanup() {
	debugLog("MAIN", "Startup cleanup: sweeping leftovers from previous session")
	// Stale PID files (processes are dead — killProcessByFile removes the file either way)
	killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel (stale)")
	killProcessByFile(Config.TempFiles.TrayPID, "Tray Monitor (stale)")
	// Orphan ssh.exe holding :1080 without any PID file
	killStaleListeners(Config.Network.ProxyPort)
	// Stale keys in the agent (service itself stays up — still needed)
	purgeSSHAgentKeys()
	// Fail-open: a crash may leave the system pointing at a dead PAC
	disableSystemProxy()
	// One-shot stop flag: a leftover from a crashed -stop must not wedge the
	// next session (its own cleanup already removes it on the happy path).
	// A fresh process start is a new intent — AutoConnect decides.
	os.Remove(Config.TempFiles.StopFlag)
	// Legacy known_hosts from pre-nul versions
	os.Remove(filepath.Join(Config.Paths.WorkDir, ".ssh", "goproxy_known_hosts"))
}

// cleanupFlags removes stale flag files from previous sessions
func cleanupFlags() {
	files := []string{
		Config.TempFiles.StopFlag,
	}

	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			if info, err := os.Stat(f); err == nil {
				if time.Since(info.ModTime()) > time.Hour {
					os.Remove(f)
				}
			}
		}
	}
}
