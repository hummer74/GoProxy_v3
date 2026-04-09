// debug.go — Unified debug logging, crash recovery, safe goroutine launcher.
// Enable with: GoProxy.exe -log
// Log file: x_goproxy_debug.log (in work directory)
// Crash file: x_goproxy_crash.log (written on ANY unhandled panic, even without -log)
package main

import (
        "fmt"
        "os"
        "path/filepath"
        "runtime"
        "sync"
        "time"
)

var (
        debugEnabled bool   // set by -log flag (set BEFORE config load in main)
        debugLogMu   sync.Mutex
        debugLogPath string // absolute path to x_goproxy_debug.log
)

// initDebugLogging enables debug logging and creates/opens the log file.
// Call this AFTER Config is loaded so Paths.WorkDir is available.
// Note: debugEnabled must already be set to true before calling this.
func initDebugLogging(workDir string) {
        debugLogPath = filepath.Join(workDir, "x_goproxy_debug.log")

        // Truncate old log file on new start (keep only current session)
        _ = os.WriteFile(debugLogPath, []byte{}, 0644)

        debugLog("INIT", "=== Debug logging started ===")
        debugLog("INIT", "WorkDir: %s", workDir)
        debugLog("INIT", "PID: %d", os.Getpid())
        debugLog("INIT", "Log file truncated for new session")
        debugLog("INIT", "GoProxy v3 starting...")
}

// debugLog writes a timestamped line to the debug log file.
// Safe to call from any goroutine. No-op when debug logging is off.
// If debugLogPath is not yet set (before config load), writes to
// the executable's directory as fallback.
func debugLog(module, format string, args ...interface{}) {
        if !debugEnabled {
                return
        }
        msg := fmt.Sprintf(format, args...)
        ts := time.Now().Format("2006-01-02 15:04:05.000")
        line := fmt.Sprintf("[%s] [%-18s] %s\n", ts, module, msg)

        debugLogMu.Lock()
        defer debugLogMu.Unlock()

        logPath := debugLogPath
        if logPath == "" {
                // Fallback: use exe directory before config is loaded
                if exe, err := os.Executable(); err == nil {
                        logPath = filepath.Join(filepath.Dir(exe), "x_goproxy_debug.log")
                } else {
                        return
                }
        }

        f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
        if err != nil {
                return
        }
        f.WriteString(line)
        f.Close()
}

// writeCrashLog writes a panic report with full stack trace.
// Called automatically on ANY panic — works even without -log flag.
func writeCrashLog(panicValue interface{}) {
        // Determine work directory (fallback to exe dir if Config not loaded yet)
        workDir := "."
        if Config != nil && Config.Paths.WorkDir != "" {
                workDir = Config.Paths.WorkDir
        } else if exe, err := os.Executable(); err == nil {
                workDir = filepath.Dir(exe)
        }

        crashPath := filepath.Join(workDir, "x_goproxy_crash.log")

        buf := make([]byte, 16384)
        n := runtime.Stack(buf, false)
        stack := string(buf[:n])

        report := fmt.Sprintf(
                "=== GoProxy Crash Report ===\n"+
                        "Time: %s\n"+
                        "Panic: %v\n\n"+
                        "Stack Trace:\n%s\n",
                time.Now().Format("2006-01-02 15:04:05"),
                panicValue,
                stack,
        )

        _ = os.WriteFile(crashPath, []byte(report), 0644)

        // Also append to debug log if enabled
        if debugEnabled {
                debugLog("CRASH", "PANIC: %v", panicValue)
                debugLog("CRASH", "Stack:\n%s", stack)
        }
}

// safeGo launches a goroutine with panic recovery.
// On panic, writes to crash log and continues running.
func safeGo(f func()) {
        go func() {
                defer func() {
                        if r := recover(); r != nil {
                                writeCrashLog(r)
                        }
                }()
                f()
        }()
}
