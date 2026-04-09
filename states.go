package main

import (
        "encoding/json"
        "fmt"
        "os"
        "time"
)

// SaveState saves the current proxy state to disk
func SaveState(state ProxyState) error {
        debugLog("STATE", "Saving state: %s -> is_chain=%v", state.Host, state.IsChain)
        data, err := json.MarshalIndent(state, "", "  ")
        if err != nil {
                return fmt.Errorf("failed to marshal state: %v", err)
        }

        if err := os.WriteFile(Config.TempFiles.StateFile, data, 0644); err != nil {
                return fmt.Errorf("failed to write state file: %v", err)
        }

        return nil
}

// LoadState loads proxy state from disk
func LoadState() (*ProxyState, error) {
        debugLog("STATE", "Loading state from: %s", Config.TempFiles.StateFile)
        data, err := os.ReadFile(Config.TempFiles.StateFile)
        if err != nil {
                return nil, fmt.Errorf("failed to read state file: %v", err)
        }

        var state ProxyState
        if err := json.Unmarshal(data, &state); err != nil {
                return nil, fmt.Errorf("failed to unmarshal state file: %v", err)
        }

        return &state, nil
}

// SaveLastHost saves the last connected host for auto-connect
func SaveLastHost(hostName string) error {
        debugLog("STATE", "Saving last host: %s", hostName)
        if err := os.WriteFile(Config.Paths.LastHostFile, []byte(hostName), 0644); err != nil {
                return fmt.Errorf("failed to write last host file: %v", err)
        }
        return nil
}

// LoadLastHost loads the last connected host
func LoadLastHost() string {
        if content, err := os.ReadFile(Config.Paths.LastHostFile); err == nil {
                debugLog("STATE", "Loaded last host: %s", string(content))
                return string(content)
        }
        return ""
}

// runStopMode stops the proxy, kills all processes, cleans up files and disables system proxy
func runStopMode() {
        debugLog("STATE", "runStopMode: killing tunnel, stopping PAC, disabling proxy")
        // 1. Create stop flag for Tray (if still running)
        os.WriteFile(Config.TempFiles.StopFlag, []byte("stop"), 0644)

        // 2. Stop processes (graceful shutdown where possible)
        killProcessByFile(Config.TempFiles.SSHTunnelPID, "SSH Tunnel")
        stopPACServer()

        // 3. Disable proxy
        disableSystemProxy()

        // 4. Wait and force stop tray
        time.Sleep(2 * time.Second)
        killProcessByFile(Config.TempFiles.TrayPID, "Tray Monitor (Force)")

        // 5. Clean up temp files
        files := []string{
                Config.TempFiles.PACFile,
                Config.TempFiles.StateFile,
                Config.TempFiles.SSHTunnelPID,
                Config.TempFiles.TrayPID,
                Config.TempFiles.PACServerPID,
                Config.TempFiles.StopFlag,
        }
        for _, f := range files {
                os.Remove(f)
        }

        printOk("All proxy services stopped and cleaned up")
}
