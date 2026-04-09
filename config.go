// config.go
package main

import (
    "fmt"
    "os"
    "path/filepath"
    "strconv"
    "time"
    "gopkg.in/ini.v1"
)

// AppConfig holds all application configuration
var Config *AppConfig

// Global variables for logging mode
var (
    isTrayMode bool // Running in tray mode
)

// AppConfig represents the application configuration structure
type AppConfig struct {
    // General settings
    General struct {
        AppName              string `ini:"AppName"`
        AutoConnect          bool   `ini:"AutoConnect"`
        AutoSelectTimeout    int    `ini:"AutoSelectTimeout"`    // Auto-select timeout in seconds
        LogSSHErrors         bool   `ini:"LogSSHErrors"`         // Enable SSH error logging
        LogTunnelEvents      bool   `ini:"LogTunnelEvents"`      // Enable tunnel event logging
        HostsCheckInterval   int    `ini:"HostsCheckInterval"`   // Hosts availability check interval in seconds for tray menu
        SmartFailover        bool   `ini:"SmartFailover"`        // Enable smart failover to fastest available host
        ReturnToOriginalHost bool   `ini:"ReturnToOriginalHost"` // Enable return to original host when available
        OriginalHostCheck    int    `ini:"OriginalHostCheck"`    // Original host check interval in seconds
        FailoverResponseTime int    `ini:"FailoverResponseTime"` // Max response time for host selection in seconds
    }

    // Network settings
    Network struct {
        ProxyPort             int `ini:"ProxyPort"`
        PACHttpPort           int `ini:"PACHttpPort"`
        SocksCheckInterval    int `ini:"SocksCheckInterval"`    // SOCKS5 check interval in seconds
        InternetCheckDelay    int `ini:"InternetCheckDelay"`    // Delay before internet check in seconds
        InternetCheckRetry    int `ini:"InternetCheckRetry"`    // Internet check retry interval in seconds
        ReconnectAttemptDelay int `ini:"ReconnectAttemptDelay"` // Reconnect attempt interval in seconds
        MaxReconnectTime      int `ini:"MaxReconnectTime"`      // Max reconnection time in seconds
    }

    // Path settings
    Paths struct {
        WorkDir        string `ini:"WorkDir"`
        SSHConfig      string `ini:"SSHConfig"`
        SSHKey         string `ini:"SSHKey"`         // SSH-KEY file path
        SSHKeyPassword string `ini:"SSHKeyPassword"` // SSH-KEY-PASSWORD file path
        PACRules       string `ini:"PACRules"`
        LastHostFile   string `ini:"LastHostFile"`
    }

    // Temp files configuration
    TempFiles struct {
        SSHTunnelPID string `ini:"SSHTunnelPID"`
        TrayPID      string `ini:"TrayPID"`
        PACServerPID string `ini:"PACServerPID"`
        StateFile    string `ini:"StateFile"`
        StopFlag     string `ini:"StopFlag"`
        PACFile      string `ini:"PACFile"`
    }
}

// HostConfig describes a host entry parsed from SSH config
type HostConfig struct {
    Name         string
    Group        string
    HostName     string
    User         string
    Port         string
    IdentityFile string
    ProxyJump    string // SSH ProxyJump directive (e.g. "x-JUMPER")
}

// ProxyState is persisted on disk and used by tray monitor and restarter
type ProxyState struct {
    IsChain          bool     `json:"is_chain"`
    Host             string   `json:"host"`
    ChainHosts       []string `json:"chain_hosts"`
    ProxyPort        int      `json:"proxy_port"`
    KeyPath          string   `json:"key_path"`
    SSHCommand       []string `json:"ssh_command"`
    RemoteHost       string   `json:"remote_host"`              // Actual host/IP from SSH config
    OriginalHost     string   `json:"original_host"`            // Original host for smart failover
    IsFailoverActive bool     `json:"is_failover_active"`       // True if we're using failover host
    FailoverStart    string   `json:"failover_start,omitempty"` // When failover was activated
}

// HostStatusWithTime holds host status with response time
type HostStatusWithTime struct {
    Host         HostConfig
    Available    bool
    ResponseTime time.Duration // Time it took to check the host (in nanoseconds)
    LastCheck    time.Time
}

// PidData describes a process tracked via PID file
type PidData struct {
    Pid  int    `json:"pid"`
    Info string `json:"info"`
}

// ANSI Colors for console output
const (
    ColorReset  = "\033[0m"
    ColorRed    = "\033[31m"
    ColorGreen  = "\033[32m"
    ColorYellow = "\033[33m"
    ColorCyan   = "\033[36m"
)

// Unicode characters for tray menu
const (
    GreenCircle  = "●" // U+25CF
    RedCircle    = "○" // U+25CB
    CurrentArrow = "→" // U+2192
    CheckMark    = "✓" // U+2713
    ChainArrow   = "▸" // U+25B8
    ClearX       = "✕" // U+2715
)

// Circled number characters for chain position display (1-10)
var CircledNumbers = []string{"①", "②", "③", "④", "⑤", "⑥", "⑦", "⑧", "⑨", "⑩"}

// GetCircledNumber returns a circled number string for the given position (1-based).
// Returns "?" if position is out of range (1-10).
func GetCircledNumber(pos int) string {
    if pos < 1 || pos > len(CircledNumbers) {
        return "?"
    }
    return CircledNumbers[pos-1]
}

// LoadConfig loads configuration from INI file or creates default
func LoadConfig(cfgPath string) error {
    Config = &AppConfig{}

    // Set default values
    setDefaultConfig()

    // Try to load INI file
    if _, err := os.Stat(cfgPath); err == nil {
        cfg, err := ini.Load(cfgPath)
        if err != nil {
            return fmt.Errorf("failed to parse INI file: %v", err)
        }

        // Map INI sections to struct
        if err := cfg.MapTo(Config); err != nil {
            return fmt.Errorf("failed to map INI to config: %v", err)
        }

        if !isTrayMode {
            fmt.Printf("%s→%s Configuration loaded from: %s\n", ColorCyan, ColorReset, cfgPath)
        }
        debugLog("CONFIG", "Config loaded from: %s", cfgPath)
    } else {
        // Create default INI file
        if err := SaveConfig(cfgPath); err != nil {
            if !isTrayMode {
                fmt.Printf("%s⚠%s Failed to save default config: %v\n", ColorYellow, ColorReset, err)
            }
        }
    }

    // Resolve relative paths
    resolvePaths()

    return nil
}

// SaveConfig saves current configuration to INI file
func SaveConfig(cfgPath string) error {
    cfg := ini.Empty()

    // Create sections and set values in the desired order

    // --- General section (order as requested) ---
    sec := cfg.Section("General")
    sec.NewKey("AppName", Config.General.AppName)
    sec.NewKey("LogSSHErrors", strconv.FormatBool(Config.General.LogSSHErrors))
    sec.NewKey("LogTunnelEvents", strconv.FormatBool(Config.General.LogTunnelEvents))
    sec.NewKey("AutoConnect", strconv.FormatBool(Config.General.AutoConnect))
    sec.NewKey("SmartFailover", strconv.FormatBool(Config.General.SmartFailover))
    sec.NewKey("ReturnToOriginalHost", strconv.FormatBool(Config.General.ReturnToOriginalHost))
    sec.NewKey("AutoSelectTimeout", strconv.Itoa(Config.General.AutoSelectTimeout))
    sec.NewKey("FailoverResponseTime", strconv.Itoa(Config.General.FailoverResponseTime))
    sec.NewKey("HostsCheckInterval", strconv.Itoa(Config.General.HostsCheckInterval))
    sec.NewKey("OriginalHostCheck", strconv.Itoa(Config.General.OriginalHostCheck))

    // --- Network section ---
    sec = cfg.Section("Network")
    sec.NewKey("ProxyPort", strconv.Itoa(Config.Network.ProxyPort))
    sec.NewKey("PACHttpPort", strconv.Itoa(Config.Network.PACHttpPort))
    sec.NewKey("SocksCheckInterval", strconv.Itoa(Config.Network.SocksCheckInterval))
    sec.NewKey("InternetCheckDelay", strconv.Itoa(Config.Network.InternetCheckDelay))
    sec.NewKey("InternetCheckRetry", strconv.Itoa(Config.Network.InternetCheckRetry))
    sec.NewKey("ReconnectAttemptDelay", strconv.Itoa(Config.Network.ReconnectAttemptDelay))
    sec.NewKey("MaxReconnectTime", strconv.Itoa(Config.Network.MaxReconnectTime))

    // --- Paths section ---
    sec = cfg.Section("Paths")
    sec.NewKey("WorkDir", Config.Paths.WorkDir)
    sec.NewKey("SSHConfig", Config.Paths.SSHConfig)
    sec.NewKey("SSHKey", Config.Paths.SSHKey)
    sec.NewKey("SSHKeyPassword", Config.Paths.SSHKeyPassword)
    sec.NewKey("PACRules", Config.Paths.PACRules)
    sec.NewKey("LastHostFile", Config.Paths.LastHostFile)

    // --- TempFiles section ---
    sec = cfg.Section("TempFiles")
    sec.NewKey("SSHTunnelPID", Config.TempFiles.SSHTunnelPID)
    sec.NewKey("TrayPID", Config.TempFiles.TrayPID)
    sec.NewKey("PACServerPID", Config.TempFiles.PACServerPID)
    sec.NewKey("StateFile", Config.TempFiles.StateFile)
    sec.NewKey("StopFlag", Config.TempFiles.StopFlag)
    sec.NewKey("PACFile", Config.TempFiles.PACFile)

    return cfg.SaveTo(cfgPath)
}

// setDefaultConfig sets default configuration values (Windows style) – updated per request
func setDefaultConfig() {
    // General settings
    Config.General.AppName = "GoProxy Manager"
    Config.General.AutoConnect = true
    Config.General.AutoSelectTimeout = 3        // 3 seconds (changed from 5)
    Config.General.LogSSHErrors = false         // Default: disable SSH error logging
    Config.General.LogTunnelEvents = true       // Enable tunnel event logging (changed from false)
    Config.General.HostsCheckInterval = 180     // Check hosts every 3 minutes (180 seconds) (changed from 120)
    Config.General.SmartFailover = false        // Disable smart failover (changed from true)
    Config.General.ReturnToOriginalHost = false // Disable return to original host (changed from true)
    Config.General.OriginalHostCheck = 30       // Check original host every 30 seconds (changed from 300)
    Config.General.FailoverResponseTime = 5     // Max response time 5 seconds (unchanged)

    // Network settings
    Config.Network.ProxyPort = 1080
    Config.Network.PACHttpPort = 8080
    Config.Network.SocksCheckInterval = 10    // Check SOCKS5 every 10 seconds (unchanged)
    Config.Network.InternetCheckDelay = 5     // Wait 5 seconds before internet check (changed from 2)
    Config.Network.InternetCheckRetry = 10    // Retry internet check every 10 seconds (changed from 5)
    Config.Network.ReconnectAttemptDelay = 20 // Reconnect attempt every 20 seconds (changed from 10)
    Config.Network.MaxReconnectTime = 7200    // Max reconnection time 2 hours (7200 seconds) (changed from 3600)

    // Path settings - relative paths in Windows style
    Config.Paths.WorkDir =  "." // Current execution directory
    Config.Paths.SSHConfig =  ".ssh\\config"
    Config.Paths.SSHKey =  ".ssh\\id_key"              // SSH-KEY file
    Config.Paths.SSHKeyPassword =  ".ssh\\id_key.pass" // SSH-KEY-PASSWORD file
    Config.Paths.PACRules =  ".ssh\\proxy_pac.txt"
    Config.Paths.LastHostFile =  ".ssh\\x_lasthost.cfg"

    // Temp files - relative paths
    Config.TempFiles.SSHTunnelPID =  "x_ssh_tunnel.pid"
    Config.TempFiles.TrayPID =  "x_tray_monitor.pid"
    Config.TempFiles.PACServerPID =  "x_http_pac.pid"
    Config.TempFiles.StateFile =  "x_proxy_state.json"
    Config.TempFiles.StopFlag =  "x_tray_stop_request.flag"
    Config.TempFiles.PACFile =  "x_proxy.pac"
}

// resolvePaths converts relative paths to absolute based on executable directory
func resolvePaths() {
    execPath, _ := os.Executable()
    execDir := filepath.Dir(execPath)

    resolvePath := func(path string) string {
        if path == "" {
            return ""
        }

        // Normalize path separators for current OS
        path = filepath.FromSlash(path)

        // Handle absolute paths
        if filepath.IsAbs(path) {
            return path
        }

        // Handle special case for WorkDir
        if path == "." || path == "."+string(filepath.Separator) {
            return execDir
        }

        // Combine with executable directory
        return filepath.Join(execDir, path)
    }

    // Resolve all paths
    Config.Paths.WorkDir = resolvePath(Config.Paths.WorkDir)
    Config.Paths.SSHConfig = resolvePath(Config.Paths.SSHConfig)
    Config.Paths.SSHKey = resolvePath(Config.Paths.SSHKey)
    Config.Paths.SSHKeyPassword = resolvePath(Config.Paths.SSHKeyPassword)
    Config.Paths.PACRules = resolvePath(Config.Paths.PACRules)
    Config.Paths.LastHostFile = resolvePath(Config.Paths.LastHostFile)

    Config.TempFiles.SSHTunnelPID = resolvePath(Config.TempFiles.SSHTunnelPID)
    Config.TempFiles.TrayPID = resolvePath(Config.TempFiles.TrayPID)
    Config.TempFiles.PACServerPID = resolvePath(Config.TempFiles.PACServerPID)
    Config.TempFiles.StateFile = resolvePath(Config.TempFiles.StateFile)
    Config.TempFiles.StopFlag = resolvePath(Config.TempFiles.StopFlag)
    Config.TempFiles.PACFile = resolvePath(Config.TempFiles.PACFile)
}

// logTunnelEvent logs tunnel events (connection established, lost, errors)
func logTunnelEvent(eventType, host, message string) {
    // Check if tunnel event logging is enabled
    if !Config.General.LogTunnelEvents {
        return
    }

    logPath := filepath.Join(Config.Paths.WorkDir, "x_goproxy_tunnel.log")
    f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
    if err != nil {
        return
    }
    defer f.Close()

    timestamp := time.Now().Format("2006-01-02 15:04:05")
    logLine := fmt.Sprintf("[%s] [%s] %s: %s\n", timestamp, eventType, host, message)
    f.WriteString(logLine)

    // Also show in console if in interactive mode
    if !isTrayMode {
        switch eventType {
        case "ERROR":
            fmt.Printf("%s✗%s %s: %s\n", ColorRed, ColorReset, host, message)
        case "OK":
            fmt.Printf("%s✓%s %s: %s\n", ColorGreen, ColorReset, host, message)
        case "WARN":
            fmt.Printf("%s⚠%s %s: %s\n", ColorYellow, ColorReset, host, message)
        default:
            fmt.Printf("%s→%s %s: %s\n", ColorCyan, ColorReset, host, message)
        }
    }

    debugLog("TUNNEL", "[%s] %s: %s", eventType, host, message)
}

// logSSHError logs SSH connection errors
func logSSHError(host, errorType, message string) {
    // Check if SSH error logging is enabled
    if !Config.General.LogSSHErrors {
        return
    }

    logPath := filepath.Join(Config.Paths.WorkDir, "x_goproxy_ssh.log")
    f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
    if err != nil {
        return
    }
    defer f.Close()

    timestamp := time.Now().Format("2006-01-02 15:04:05")
    logLine := fmt.Sprintf("[%s] [SSH_ERROR] %s (%s): %s\n", timestamp, host, errorType, message)
    f.WriteString(logLine)

    // Also show in console if in interactive mode
    if !isTrayMode {
        fmt.Printf("%s✗%s SSH error %s (%s): %s\n", ColorRed, ColorReset, host, errorType, message)
    }

    debugLog("SSH", "[%s] %s (%s): %s", host, errorType, message)
}

// Helpers for status output (kept for backward compatibility)
func printOk(msg string) {
    if !isTrayMode {
        fmt.Printf("%s✓%s %s\n", ColorGreen, ColorReset, msg)
    }
}

func printErr(msg string) {
    if !isTrayMode {
        fmt.Printf("%s✗%s %s\n", ColorRed, ColorReset, msg)
    }
}

func printWarn(msg string) {
    if !isTrayMode {
        fmt.Printf("%s⚠%s %s\n", ColorYellow, ColorReset, msg)
    }
}

func printInfo(msg string) {
    if !isTrayMode {
        fmt.Printf("%s→%s %s\n", ColorCyan, ColorReset, msg)
    }
}
