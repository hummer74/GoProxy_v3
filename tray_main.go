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
    currentHost        string // Currently connected host
    isTunnelActive     bool   // Tunnel activity flag
    monitoringActive   bool   // Monitoring activity flag
    monitoringMutex    sync.Mutex
    monitoringStopChan chan bool
    menuUpdateTicker   *time.Ticker
    hostsCheckTicker   *time.Ticker // Ticker for periodic hosts checking
    tunnelStartTime    time.Time
)

// runTrayMode runs the system tray application
func runTrayMode() {
    // If launched directly from console (e.g. "GoProxy" without flags),
    // re-launch ourselves as a detached background process and exit
    // immediately — this releases the console back to cmd.exe.
    //
    // When launched from interactive mode via exec.Command + CREATE_NO_WINDOW,
    // GetConsoleWindow() returns 0 (no console) — we skip re-launch.
    kernel32 := windows.NewLazySystemDLL("kernel32.dll")
    getConsoleWindow := kernel32.NewProc("GetConsoleWindow")
    hwnd, _, _ := getConsoleWindow.Call()

    if hwnd != 0 {
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
}

// onTrayReady initializes the system tray
func onTrayReady() {
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

    // Clear any existing menu by reassigning variables
    hostMenuItems = make(map[string]*systray.MenuItem)
    hostStatusCache = NewHostStatusCache()

    // Build the initial menu
    createTrayMenu()

    // Start PAC server
    go startPACServerInternal()

    // Start menu update ticker (only for UI updates)
    menuUpdateTicker = time.NewTicker(1 * time.Second)
    go menuUpdateLoop()

    // Start periodic hosts checking
    startPeriodicHostsChecking()

    // Check if tunnel is already running (from interactive mode)
    checkAndRestoreExistingTunnel()
}

// onTrayExit handles tray application exit
func onTrayExit() {
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

    go func() {
        for range hostsCheckTicker.C {
            updateAllHostsStatus()
        }
    }()
}

// updateAllHostsStatus updates status of all hosts in the menu
func updateAllHostsStatus() {
    // Parse SSH config to get hosts
    hosts := parseSSHConfig(Config.Paths.SSHConfig)
    if len(hosts) == 0 {
        return
    }

    // Get first group hosts (like in interactive mode)
    firstGroup := hosts[0].Group
    var firstGroupHosts []HostConfig
    for _, h := range hosts {
        if h.Group == firstGroup {
            firstGroupHosts = append(firstGroupHosts, h)
        }
    }

    // Update hosts status in menu
    updateHostStatusInMenu(firstGroupHosts, false)
}
