// ssh_command.go
package main

import (
        "fmt"
        "os"
        "path/filepath"
        "strings"
)

// buildSSHCommand constructs the full SSH tunnel command for single host or chain.
//
// For reverse hosts (hosts with ProxyJump whose target is in the chain),
// ProxyJump is written into the temporary SSH config instead of using the
// -J flag. This produces cleaner commands:
//
//   Before: ssh -F config ... -J x-JUMPER J-04-FN
//   After:  ssh -F config ... J-04-FN   (ProxyJump x-JUMPER is in the config)
//
// For manually built chains where the final host's ProxyJump target is NOT
// in the chain, the -J flag is used as before.
func buildSSHCommand(hosts []HostConfig, keyPath string) []string {
        sshDir := filepath.Join(Config.Paths.WorkDir, ".ssh")
        knownHostsPath := filepath.Join(sshDir, "goproxy_known_hosts")
        sshConfigPath := filepath.Join(sshDir, "goproxy_ssh_config")

        // Ensure .ssh folder exists
        _ = os.MkdirAll(sshDir, 0755)

        // Base config that applies to all hosts
        configContent := fmt.Sprintf(`Host *
    AddressFamily inet
    BatchMode yes
    ControlMaster auto
    TCPKeepAlive no
    ControlPersist 1m
    ServerAliveInterval 5
    ServerAliveCountMax 6
    StrictHostKeyChecking accept-new
    UserKnownHostsFile "%s"
    ExitOnForwardFailure yes
    LogLevel INFO
    RequestTTY no

`, filepath.ToSlash(knownHostsPath))

        // Build a set of host names in the chain for quick lookup
        hostSet := make(map[string]bool)
        for _, h := range hosts {
                hostSet[h.Name] = true
        }

        // Determine if the final host has ProxyJump and the target is in our chain.
        // If so, we write ProxyJump in the config and skip the -J flag.
        finalHost := hosts[len(hosts)-1]
        useConfigProxyJump := finalHost.ProxyJump != "" && hostSet[finalHost.ProxyJump]
        debugLog("SSH-CMD", "Building command for %d hosts, useConfigProxyJump=%v", len(hosts), useConfigProxyJump)

        // Add per-host entries so each jump host can use individual identity/port/user
        for idx, h := range hosts {
                alias := h.Name
                if alias == "" {
                        alias = fmt.Sprintf("goproxy-host-%d", idx)
                }
                alias = strings.ReplaceAll(alias, " ", "_")

                hostname := h.HostName
                if hostname == "" {
                        hostname = h.Name
                }

                user := h.User
                if user == "" {
                        user = "root"
                }

                port := h.Port
                if port == "" {
                        port = "22"
                }

                identity := keyPath
                if h.IdentityFile != "" {
                        resolved := resolveSSHKeyPath(Config.Paths.WorkDir, h.IdentityFile)
                        if resolved != "" {
                                identity = resolved
                        }
                }

                configContent += fmt.Sprintf("Host %s\n", alias)
                configContent += fmt.Sprintf("    HostName %s\n", hostname)
                configContent += fmt.Sprintf("    User %s\n", user)
                configContent += fmt.Sprintf("    Port %s\n", port)
                if identity != "" {
                        configContent += fmt.Sprintf("    IdentityFile %s\n", filepath.ToSlash(identity))
                }

                // For the final host with matching ProxyJump: use ProxyJump directive.
                // This lets SSH natively resolve the jump chain from the config.
                // For all other hosts: clear any inherited proxy with ProxyCommand none.
                if useConfigProxyJump && h.Name == finalHost.Name {
                        configContent += fmt.Sprintf("    ProxyJump %s\n", h.ProxyJump)
                } else {
                        configContent += "    ProxyCommand none\n"
                }
                configContent += "\n"
        }

        _ = os.WriteFile(sshConfigPath, []byte(configContent), 0644)
        debugLog("SSH-CMD", "Temp config written: %s", sshConfigPath)

        cmd := []string{"ssh", "-F", sshConfigPath, "-N", "-T", "-4", "-D", fmt.Sprintf("127.0.0.1:%d", Config.Network.ProxyPort)}

        finalAlias := finalHost.Name
        if finalAlias == "" {
                finalAlias = fmt.Sprintf("goproxy-host-%d", len(hosts)-1)
        }
        finalAlias = strings.ReplaceAll(finalAlias, " ", "_")

        if useConfigProxyJump {
                // SSH will follow ProxyJump from config — no -J flag needed
                cmd = append(cmd, finalAlias)
        } else if len(hosts) > 1 {
                // Manual chain — use -J flag for jump hosts
                var jumpAliases []string
                for i := 0; i < len(hosts)-1; i++ {
                        alias := hosts[i].Name
                        if alias == "" {
                                alias = fmt.Sprintf("goproxy-host-%d", i)
                        }
                        alias = strings.ReplaceAll(alias, " ", "_")
                        jumpAliases = append(jumpAliases, alias)
                }
                cmd = append(cmd, "-J", strings.Join(jumpAliases, ","), finalAlias)
        } else {
                // Single host — direct connection
                cmd = append(cmd, finalAlias)
        }

        logTunnelEvent("DEBUG", "SSH Command", fmt.Sprintf("%v", cmd))
        debugLog("SSH-CMD", "Final command: %v", cmd)

        return cmd
}
