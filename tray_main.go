package main

import (
    "fmt"
    "image/color"
    "os"
    "os/exec"
    "sync"
    "time"

    "github.com/getlantern/systray"
    windows "golang.org/x/sys/windows"
)

// Global variables for menu and monitoring
var (
    // Tunnel connection state is managed by connState (see connection_state.go)
    monitoringActive   bool   // Monitoring activity flag
    monitoringMutex    sync.Mutex
    monitoringStopChan chan bool
    menuUpdateTicker   *time.Ticker
    hostsCheckTicker   *time.Ticker // Ticker for periodic hosts checking
)

// runTrayMode runs the system tray application
func runTrayMode() {
    debugLog("TRAY", "runTrayMode called")
    // If launched directly from console (e.g. "GoProxy" without flags),
    // re-launch ourselves as a detached background process and exit
    // immediately — this releases the console back to cmd.exe.
    //
    // When launched from interactive mode via exec.Command + CREATE_NO_WINDOW,
    // GetConsoleWindow() returns 0 (no console) — we skip re-launch.
    kernel32 := windows.NewLazySystemDLL("kernel32.dll")
    getConsoleWindow := kernel32.NewProc("GetConsoleWindow")
    hwnd, _, _ := getConsoleWindow.Call()

    if hwnd != 0 && !debugEnabled {
        debugLog("TRAY", "Console detected, re-launching without window")
        ReleaseAppMutex() // Release mutex BEFORE child tries to acquire it
        exe, _ := os.Executable()
        cmd := exec.Command(exe)
        cmd.SysProcAttr = &windows.SysProcAttr{
            CreationFlags: windows.CREATE_NO_WINDOW,
        }
        cmd.Start()
        return // exit runTrayMode() → main() returns → process dies → console freed
    }

    defer func() {
        if r := recover(); r != nil {
            // Log panic to file for debugging
            os.WriteFile("tray_crash.log", []byte(fmt.Sprintf("Tray panic: %v\n", r)), 0644)
        }
    }()

    savePid(Config.TempFiles.TrayPID, os.Getpid(), "Tray")

    // Run systray with error handling
    systray.Run(onTrayReady, onTrayExit)
    debugLog("TRAY", "systray.Run exited")
}

// onTrayReady initializes the system tray
func onTrayReady() {
    debugLog("TRAY", "onTrayReady()")
    defer func() {
        if r := recover(); r != nil {
            // Log initialization error
            os.WriteFile("tray_init_error.log", []byte(fmt.Sprintf("onTrayReady panic: %v\n", r)), 0644)
            // Try to show basic tray anyway
            systray.SetTitle("GoProxy - Error")
            systray.SetTooltip("Failed to initialize menu")
        }
    }()

    // Load default icon (yellow) on startup
    iconData := loadIconData(color.RGBA{255, 255, 0, 255})
    if iconData != nil {
        systray.SetIcon(iconData)
    } else {
        // Fallback: use empty icon if embedded icons are missing
        systray.SetIcon([]byte{})
    }

    systray.SetTitle("SOCKS5: Ready")
    systray.SetTooltip("Ready to connect")

    // Initialize stop channel first
    monitoringStopChan = make(chan bool, 1)
    debugLog("TRAY", "Stop channel initialized")

    // Clear any existing menu by reassigning variables
    hostMenuItems = make(map[string]*systray.MenuItem)
    hostStatusCache = NewHostStatusCache()

    // Build the initial menu
    createTrayMenu()
    debugLog("TRAY", "Menu created")

    // Start PAC server
    safeGo(startPACServerInternal)
    debugLog("TRAY", "PAC server started")

    // Start menu update ticker (only for UI updates)
    menuUpdateTicker = time.NewTicker(1 * time.Second)
    safeGo(menuUpdateLoop)
    debugLog("TRAY", "Menu update loop started")

    // Start periodic hosts checking
    startPeriodicHostsChecking()

    // Check if tunnel is already running (from interactive mode)
    checkAndRestoreExistingTunnel()
    debugLog("TRAY", "Existing tunnel check done")
}

// onTrayExit handles tray application exit
func onTrayExit() {
    debugLog("TRAY", "onTrayExit()")
    // Stop monitoring
    stopMonitoring()

    // Stop menu update ticker
    if menuUpdateTicker != nil {
        menuUpdateTicker.Stop()
    }

    // Stop hosts check ticker
    if hostsCheckTicker != nil {
        hostsCheckTicker.Stop()
    }

    // Clean up PID file
    os.Remove(Config.TempFiles.TrayPID)
}

// startPeriodicHostsChecking starts periodic checking of hosts availability
func startPeriodicHostsChecking() {
    if hostsCheckTicker != nil {
        hostsCheckTicker.Stop()
    }

    // Use configuration value, default is 300 seconds (5 minutes)
    interval := time.Duration(Config.General.HostsCheckInterval) * time.Second
    if interval < 30*time.Second {
        interval = 30 * time.Second // Minimum 30 seconds
    }

    hostsCheckTicker = time.NewTicker(interval)
    debugLog("TRAY", "Hosts check interval: %ds", int(interval.Seconds()))

    safeGo(func() {
        for range hostsCheckTicker.C {
            updateAllHostsStatus()
        }
    })
}

// updateAllHostsStatus updates status of all hosts in the menu (all groups)
func updateAllHostsStatus() {
    // Parse SSH config to get all hosts
    hosts := parseSSHConfig(Config.Paths.SSHConfig)
    debugLog("TRAY", "Updating all hosts status (%d hosts)", len(hosts))
    if len(hosts) == 0 {
        return
    }

    // Update status for ALL hosts across all groups
    updateHostStatusInMenu(hosts, false)
}
