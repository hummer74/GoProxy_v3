// ssh_command.go
package main

import (
        "fmt"
        "os"
        "os/exec"
        "path/filepath"
        "regexp"
        "strings"
        "sync"

        windows "golang.org/x/sys/windows"
)

// sshVersionInfo caches the detected SSH version (parsed once, used many times)
var (
        sshVersionMajor int
        sshVersionMinor int
        sshVersionOnce  sync.Once
        sshVersionReady bool
)

// getSSHVersion parses the local SSH version via "ssh -V".
// Returns (major, minor) and caches the result.
// On failure returns (0, 0) — safest defaults will be used.
func getSSHVersion() (int, int) {
        sshVersionOnce.Do(func() {
                cmd := exec.Command("ssh", "-V")
                cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
                output, err := cmd.CombinedOutput()
                if err != nil {
                        debugLog("SSH-VER", "Failed to get SSH version: %v", err)
                        return
                }

                // OpenSSH_for_Windows_8.6p1, LibreSSL 3.4.3
                // OpenSSH_9.5p1, OpenSSL 3.3.1
                // ssh: OpenSSH_9.8p1 ...
                re := regexp.MustCompile(`OpenSSH[_ ](?:for_Windows_)?(\d+)\.(\d+)`)
                matches := re.FindStringSubmatch(string(output))
                if len(matches) >= 3 {
                        fmt.Sscanf(matches[1], "%d", &sshVersionMajor)
                        fmt.Sscanf(matches[2], "%d", &sshVersionMinor)
                        sshVersionReady = true
                        debugLog("SSH-VER", "Detected SSH version: %d.%d", sshVersionMajor, sshVersionMinor)
                } else {
                        debugLog("SSH-VER", "Could not parse SSH version from: %q", string(output))
                }
        })
        return sshVersionMajor, sshVersionMinor
}

// sshVersionAtLeast returns true if the local SSH version is >= (major.minor).
func sshVersionAtLeast(major, minor int) bool {
        m, n := getSSHVersion()
        if !sshVersionReady {
                return false // Unknown version — use safest defaults
        }
        if m != major {
                return m > major
        }
        return n >= minor
}

// buildSSHConfigForHosts builds a minimal SSH config for the given hosts.
// It preserves the Host * section from the original config and includes only
// the host blocks required for the current connection.
func buildSSHConfigForHosts(configPath string, hosts []HostConfig, knownHostsPath string) (string, error) {
        data, err := os.ReadFile(configPath)
        if err != nil {
                return "", err
        }

        allHosts := parseSSHConfig(configPath)
        hostMap := make(map[string]HostConfig)
        for _, h := range allHosts {
                hostMap[h.Name] = h
        }

        relevant := make(map[string]bool)
        queue := []string{}
        for _, h := range hosts {
                if h.Name != "" {
                        relevant[h.Name] = true
                        queue = append(queue, h.Name)
                }
                if h.ProxyJump != "" && !relevant[h.ProxyJump] {
                        relevant[h.ProxyJump] = true
                        queue = append(queue, h.ProxyJump)
                }
        }
        for i := 0; i < len(queue); i++ {
                name := queue[i]
                if cfg, ok := hostMap[name]; ok && cfg.ProxyJump != "" && !relevant[cfg.ProxyJump] {
                        relevant[cfg.ProxyJump] = true
                        queue = append(queue, cfg.ProxyJump)
                }
        }

        type sshBlock struct {
                isGlobal bool
                patterns []string
                lines    []string
        }

        var blocks []sshBlock
        var current *sshBlock
        appendCurrent := func() {
                if current == nil {
                        return
                }
                blocks = append(blocks, *current)
                current = nil
        }

        normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
        for _, raw := range strings.Split(normalized, "\n") {
                trimmed := strings.TrimSpace(raw)
                if strings.HasPrefix(trimmed, "Host ") {
                        appendCurrent()
                        current = &sshBlock{lines: []string{raw}}
                        patternStr := strings.TrimSpace(trimmed[5:])
                        current.patterns = strings.Fields(patternStr)
                        if len(current.patterns) == 1 && current.patterns[0] == "*" {
                                current.isGlobal = true
                        }
                        continue
                }
                if current != nil {
                        current.lines = append(current.lines, raw)
                }
        }
        appendCurrent()

        configDir := filepath.Dir(configPath)

        var globalBlock *sshBlock
        for idx := range blocks {
                if blocks[idx].isGlobal {
                        globalBlock = &blocks[idx]
                        break
                }
        }

        included := make(map[int]bool)
        for idx, b := range blocks {
                if b.isGlobal {
                        continue
                }
                for _, pattern := range b.patterns {
                        if relevant[pattern] {
                                included[idx] = true
                                break
                        }
                }
        }

        result := []string{}
        if globalBlock != nil {
                userKnownHostsFound := false
                for _, raw := range globalBlock.lines {
                        trimmed := strings.TrimSpace(raw)
                        if strings.HasPrefix(strings.ToLower(trimmed), "userknownhostsfile") {
                                result = append(result, raw) // preserve original value
                                userKnownHostsFound = true
                                continue
                        }
                        result = append(result, raw)
                }
                if !userKnownHostsFound {
                        result = append(result, fmt.Sprintf("    UserKnownHostsFile \"%s\"", filepath.ToSlash(knownHostsPath)))
                }
        } else {
                result = append(result,
                        "Host *",
                        fmt.Sprintf("    UserKnownHostsFile \"%s\"", filepath.ToSlash(knownHostsPath)),
                )
        }

        for idx, b := range blocks {
                if !included[idx] {
                        continue
                }
                result = append(result, "")
                for _, raw := range b.lines {
                        trimmed := strings.TrimSpace(raw)
                        low := strings.ToLower(trimmed)
                        if strings.HasPrefix(low, "identityfile ") {
                                parts := strings.SplitN(trimmed, " ", 2)
                                if len(parts) == 2 {
                                        keyPath := strings.Trim(parts[1], "\"')")
                                        resolved := keyPath
                                        if !filepath.IsAbs(keyPath) {
                                                candidate := filepath.Join(configDir, keyPath)
                                                if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
                                                        resolved = candidate
                                                }
                                        }
                                        result = append(result, fmt.Sprintf("    IdentityFile %s", filepath.ToSlash(resolved)))
                                        continue
                                }
                        }
                        result = append(result, raw)
                }
        }

        return strings.Join(result, "\n"), nil
}

// buildSSHCommand constructs the full SSH tunnel command for single host or chain.
//
// Builds a short temporary config containing the global Host * block and only the
// host entries required for the current connection. All host-specific values are
// taken from the original SSH config. If the original config does not contain
// UserKnownHostsFile, the temporary config defaults to "nul" on Windows.
func buildSSHCommand(hosts []HostConfig, keyPath string) []string {
        sshDir := filepath.Join(Config.Paths.WorkDir, ".ssh")
        knownHostsPath := "nul"
        sshConfigPath := filepath.Join(sshDir, "goproxy_ssh_config")

        // Ensure .ssh folder exists
        _ = os.MkdirAll(sshDir, 0755)

        configContent, err := buildSSHConfigForHosts(Config.Paths.SSHConfig, hosts, knownHostsPath)
        if err != nil {
                debugLog("SSH-CMD", "Failed to build SSH config from original file: %v", err)
                configContent = fmt.Sprintf(`Host *
    AddressFamily inet
    BatchMode yes
    ControlMaster auto
    TCPKeepAlive no
    ControlPersist 1m
    ServerAliveInterval 5
    ServerAliveCountMax 2
    StrictHostKeyChecking no
    UserKnownHostsFile "%s"
    ExitOnForwardFailure yes
    LogLevel INFO
    RequestTTY no

`, filepath.ToSlash(knownHostsPath))
                debugLog("SSH-CMD", "Using fallback short config for: %s", sshConfigPath)
        }
        _ = os.WriteFile(sshConfigPath, []byte(configContent), 0644)
        debugLog("SSH-CMD", "GoProxy SSH config written: %s", sshConfigPath)

        // Build a set of host names in the chain for quick lookup
        hostSet := make(map[string]bool)
        for _, h := range hosts {
                hostSet[h.Name] = true
        }

        // Determine if the final host has ProxyJump and the target is in our chain.
        // Since we're using the config directly, ProxyJump from config will be used if present.
        finalHost := hosts[len(hosts)-1]
        useConfigProxyJump := finalHost.ProxyJump != "" && hostSet[finalHost.ProxyJump]
        debugLog("SSH-CMD", "Building command for %d hosts, useConfigProxyJump=%v", len(hosts), useConfigProxyJump)

        cmd := []string{"ssh", "-F", sshConfigPath, "-N", "-T", "-4", "-D", fmt.Sprintf("127.0.0.1:%d", Config.Network.ProxyPort)}

        // Add global identity if specified
        if keyPath != "" {
                cmd = append(cmd, "-i", keyPath)
        }

        finalHostName := finalHost.Name
        if finalHostName == "" {
                finalHostName = finalHost.HostName
        }

        if useConfigProxyJump {
                // SSH will follow ProxyJump from config — no -J flag needed
                cmd = append(cmd, finalHostName)
        } else if len(hosts) > 1 {
                // Manual chain — use -J flag for jump hosts
                var jumpHosts []string
                for i := 0; i < len(hosts)-1; i++ {
                        jumpHost := hosts[i].Name
                        if jumpHost == "" {
                                jumpHost = hosts[i].HostName
                        }
                        jumpHosts = append(jumpHosts, jumpHost)
                }
                cmd = append(cmd, "-J", strings.Join(jumpHosts, ","), finalHostName)
        } else {
                // Single host — direct connection
                cmd = append(cmd, finalHostName)
        }

        logTunnelEvent("DEBUG", "SSH Command", fmt.Sprintf("%v", cmd))
        debugLog("SSH-CMD", "Final command: %v", cmd)

        return cmd
}

// replaceSSHTunnelPort replaces the -D SOCKS port in an SSH tunnel command.
// Returns a new slice; the original is unchanged.
func replaceSSHTunnelPort(sshCmd []string, newPort int) []string {
        out := make([]string, len(sshCmd))
        copy(out, sshCmd)
        for i := 0; i < len(out)-1; i++ {
                if out[i] == "-D" {
                        out[i+1] = fmt.Sprintf("127.0.0.1:%d", newPort)
                        return out
                }
        }
        return out
}
