package main

import (
    "flag"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "time"

    windows "golang.org/x/sys/windows"
)

var appMutex windows.Handle // Global mutex handle

// createAppMutex creates named mutex to prevent multiple instances
func createAppMutex() (windows.Handle, error) {
    // Create unique mutex name based on executable path
    execPath, _ := os.Executable()
    mutexName := fmt.Sprintf("Global\\GoProxyMutex_%x", filepath.Base(execPath))

    // Try to create mutex
    mutex, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(mutexName))
    if err != nil {
        // If mutex already exists, application is already running
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
    // Create mutex for blocking multiple instances
    _, err := createAppMutex()
    if err != nil {
        fmt.Printf("Error: %v\n", err)
        fmt.Println("GoProxy is already running in another instance.")
        fmt.Println("If this is incorrect, please wait a moment and try again.")
        os.Exit(1)
    }
    defer ReleaseAppMutex()

    // Parse command line arguments
    cliFlag := flag.Bool("cli", false, "Interactive CLI mode")
    stopFlag := flag.Bool("stop", false, "Stop proxy and cleanup")
    flag.Parse()

    // Determine mode: default=tray, -cli=interactive, -stop=stop
    var mode string
    if *stopFlag {
        mode = "stop"
    } else if *cliFlag {
        mode = "interactive"
    } else {
        mode = "tray"
    }

    // Set mode flag based on command line argument
    if mode == "tray" {
        isTrayMode = true
    }

    // Enable ANSI color support in Windows console (only for interactive mode)
    if mode == "interactive" {
        enableVirtualTerminalProcessing()
    }

    // Initialize configuration
    execPath, _ := os.Executable()
    workDir := filepath.Dir(execPath)
    cfgPath := filepath.Join(workDir, "GoProxy.ini")

    if err := LoadConfig(cfgPath); err != nil {
        if !isTrayMode {
            fmt.Printf("Warning: Could not load config file: %v\n", err)
            fmt.Println("Using default configuration...")
        }
    }

    // Clean up any stale flag files on start
    cleanupFlags()

    // Check and start SSH-Agent service on program start (except for stop mode)
    if mode != "stop" {
        if !isTrayMode {
            printInfo("Checking SSH-Agent service...")
        }
        if !checkAndStartSSHAgent() {
            if !isTrayMode {
                printWarn("SSH-Agent service check failed. SSH connections may not work properly.")
                printWarn("You can try to start it manually as Administrator:")
                printWarn("  sc config ssh-agent start=auto")
                printWarn("  sc start ssh-agent")
            }
        }
        defer cleanupSSHAgent()
    }

    // Run the selected mode
    switch mode {
    case "stop":
        runStopMode()
    case "tray":
        runTrayMode()
    case "interactive":
        runInteractiveMode()
    }
}

// enableVirtualTerminalProcessing enables ANSI color support in the Windows console
func enableVirtualTerminalProcessing() {
    handle := windows.Handle(os.Stdout.Fd())
    var mode uint32
    const ENABLE_VIRTUAL_TERMINAL_PROCESSING uint32 = 0x0004
    if err := windows.GetConsoleMode(handle, &mode); err == nil {
        _ = windows.SetConsoleMode(handle, mode|ENABLE_VIRTUAL_TERMINAL_PROCESSING)
    }
}

// cleanupFlags removes stale flag files from previous sessions
func cleanupFlags() {
    files := []string{
        Config.TempFiles.StopFlag,
    }

    for _, f := range files {
        if _, err := os.Stat(f); err == nil {
            // Check if file is old (more than 1 hour)
            if info, err := os.Stat(f); err == nil {
                if time.Since(info.ModTime()) > time.Hour {
                    os.Remove(f)
                }
            }
        }
    }
}

// startTrayProcess launches the current executable in tray mode without a console window
func startTrayProcess(execPath string) {
    if !isTrayMode {
        printInfo("Attempting to launch Tray Monitor...")
    }

    // No flags needed — default mode is tray
    cmd := exec.Command(execPath)
    cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
    if err := cmd.Start(); err == nil {
        if !isTrayMode {
            printOk(fmt.Sprintf("Tray Monitor started with PID %d. Please check the tray icon.", cmd.Process.Pid))
        }
    } else {
        if !isTrayMode {
            printErr(fmt.Sprintf("Failed to launch Tray Monitor: %v", err))
        }
    }
}
