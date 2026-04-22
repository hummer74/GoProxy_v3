// main.go
package main

import (
        "flag"
        "fmt"
        "os"
        "path/filepath"
        "time"

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

        // ── Parse command line flags ──
        logFlag := flag.Bool("log", false, "Enable debug logging (x_goproxy_debug.log)")
        stopFlag := flag.Bool("stop", false, "Stop proxy and cleanup")
        flag.Parse()

        if *logFlag {
                debugEnabled = true
                fmt.Println("=== GoProxy Debug Mode ===")
                fmt.Println("Logging to x_goproxy_debug.log")
                fmt.Println("Crash logs to x_goproxy_crash.log")
                fmt.Println("=========================")
        }

        // ── Create mutex for blocking multiple instances ──
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

        // ── Load configuration ──
        execPath, _ := os.Executable()
        workDir := filepath.Dir(execPath)
        cfgPath := filepath.Join(workDir, "GoProxy.ini")

        debugLog("MAIN", "Executable: %s", execPath)
        debugLog("MAIN", "WorkDir: %s", workDir)
        debugLog("MAIN", "Config path: %s", cfgPath)

        if err := LoadConfig(cfgPath); err != nil {
                fmt.Printf("Warning: Could not load config file: %v\n", err)
                fmt.Println("Using default configuration...")
                debugLog("MAIN", "Config load error: %v", err)
        }

        // ── Initialize debug logging AFTER config is loaded ──
        if *logFlag {
                initDebugLogging(Config.Paths.WorkDir)
        }

        debugLog("MAIN", "GoProxy starting (PID %d)", os.Getpid())
        debugLog("MAIN", "debugEnabled=%v, stopFlag=%v", *logFlag, *stopFlag)
        debugLog("MAIN", "Config: WorkDir=%s ProxyPort=%d PACPort=%d",
                Config.Paths.WorkDir, Config.Network.ProxyPort, Config.Network.PACHttpPort)

        // ── Clean up stale flag files ──
        cleanupFlags()

        // ── Check and start SSH-Agent (except for stop mode) ──
        if !*stopFlag {
                debugLog("MAIN", "Checking SSH-Agent service...")
                if !checkAndStartSSHAgent() {
                        debugLog("MAIN", "SSH-Agent check failed")
                } else {
                        debugLog("MAIN", "SSH-Agent service is running")
                }
                defer cleanupSSHAgent()
        }

        // ── Load priority host from x_lasthost.cfg (if available) ──
        debugLog("MAIN", "Loading priority host from x_lasthost.cfg...")
        if loadedHost := LoadPriorityHost(); loadedHost != "" {
        	debugLog("MAIN", "Priority host loaded: %s", loadedHost)
        } else {
        	debugLog("MAIN", "No priority host found in x_lasthost.cfg")
        }
       
        // ── Run selected mode ──
        if *stopFlag {
                debugLog("MAIN", "Mode: STOP")
                runStopMode()
        } else {
                debugLog("MAIN", "Mode: TRAY")
                runTrayMode()
        }
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
