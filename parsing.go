package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// parseSSHConfig reads and parses the SSH config file
func parseSSHConfig(path string) []HostConfig {
	file, err := os.Open(path)
	if err != nil {
		printWarn(fmt.Sprintf("Failed to open SSH config: %v", err))
		return []HostConfig{}
	}
	defer file.Close()

	var hosts []HostConfig
	var current HostConfig
	var currentGroup = "Uncategorized"

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Detect group headers (####Group Name####)
		if strings.HasPrefix(line, "####") && strings.HasSuffix(line, "####") {
			if scanner.Scan() {
				nl := strings.TrimSpace(scanner.Text())
				if strings.HasPrefix(nl, "# ") {
					currentGroup = nl[2:]
				}
			}
			continue
		}

		// Start of a new Host block
		if strings.HasPrefix(line, "Host ") {
			// Save previous host if exists
			if current.Name != "" && current.Name != "*" {
				hosts = append(hosts, current)
			}

			// Start new host
			current = HostConfig{
				Name:  strings.TrimSpace(line[5:]),
				Group: currentGroup,
			}
		} else if current.Name != "" && line != "" && !strings.HasPrefix(line, "#") {
			// Parse host parameters
			parts := strings.SplitN(line, " ", 2)
			if len(parts) == 2 {
				key, value := strings.ToLower(parts[0]), strings.TrimSpace(parts[1])

				switch key {
				case "hostname":
					current.HostName = value
				case "user":
					current.User = value
				case "port":
					current.Port = value
				case "identityfile":
					if current.IdentityFile == "" {
						current.IdentityFile = value
					}
				default:
					// Ignore other parameters
				}
			}
		}
	}

	// Add the last host if exists
	if current.Name != "" && current.Name != "*" {
		hosts = append(hosts, current)
	}

	printInfo(fmt.Sprintf("Parsed %d hosts from SSH config", len(hosts)))
	return hosts
}
