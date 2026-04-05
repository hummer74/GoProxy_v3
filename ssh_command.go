// ssh_command.go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// buildSSHCommand constructs the full SSH tunnel command for single host or chain
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
    ConnectTimeout 15
    ServerAliveInterval 30
    ServerAliveCountMax 3
    TCPKeepAlive yes
    StrictHostKeyChecking accept-new
    UserKnownHostsFile "%s"
    ExitOnForwardFailure yes
    LogLevel INFO
    RequestTTY no

`, filepath.ToSlash(knownHostsPath))

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
		configContent += "    ProxyCommand none\n" // clear inherited ProxyCommand if any
		configContent += "\n"
	}

	_ = os.WriteFile(sshConfigPath, []byte(configContent), 0644)

	cmd := []string{"ssh", "-F", sshConfigPath, "-N", "-T", "-4", "-D", fmt.Sprintf("127.0.0.1:%d", Config.Network.ProxyPort)}

	var jumpAliases []string
	for i := 0; i < len(hosts)-1; i++ {
		alias := hosts[i].Name
		if alias == "" {
			alias = fmt.Sprintf("goproxy-host-%d", i)
		}
		alias = strings.ReplaceAll(alias, " ", "_")
		jumpAliases = append(jumpAliases, alias)
	}

	finalAlias := hosts[len(hosts)-1].Name
	if finalAlias == "" {
		finalAlias = fmt.Sprintf("goproxy-host-%d", len(hosts)-1)
	}
	finalAlias = strings.ReplaceAll(finalAlias, " ", "_")

	if len(jumpAliases) > 0 {
		cmd = append(cmd, "-J", strings.Join(jumpAliases, ","), finalAlias)
	} else {
		cmd = append(cmd, finalAlias)
	}

	logTunnelEvent("DEBUG", "SSH Command", fmt.Sprintf("%v", cmd))

	return cmd
}
