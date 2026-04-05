package main

import (
    "fmt"
    "os"
    "os/exec"
    "time"

    windows "golang.org/x/sys/windows"
)

// LaunchTrayAndExit starts a new tray process and exits the current one.
// It releases the application mutex to allow the new instance to start.
// Returns true if the tray process was successfully launched, false otherwise.
func LaunchTrayAndExit() bool {
    // Check if tray is already running (by PID file)
    if _, err := os.Stat(Config.TempFiles.TrayPID); err == nil {
        // Tray PID file exists, assume tray is already active
        // Just exit current process without launching another tray
        printInfo("Tray already running, exiting interactive mode...")
        os.Exit(0)
        return true
    }

    // Release the mutex so the new tray process can acquire it
    ReleaseAppMutex()

    // Get current executable path
    execPath, err := os.Executable()
    if err != nil {
        printErr(fmt.Sprintf("Failed to get executable path: %v", err))
        return false
    }

    // Build command — no flags needed, default mode is tray
    cmd := exec.Command(execPath)
    cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}

    // Start the tray process
    if err := cmd.Start(); err != nil {
        printErr(fmt.Sprintf("Failed to launch tray process: %v", err))
        return false
    }

    printOk(fmt.Sprintf("Tray process started with PID %d. Exiting interactive mode.", cmd.Process.Pid))

    // Give the child process a moment to initialize
    time.Sleep(200 * time.Millisecond)

    // Exit the current process
    os.Exit(0)
    return true
}
