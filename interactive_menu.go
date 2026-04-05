// interactive_menu.go
package main

import (
	"fmt"
)

// runInteractiveMode runs the interactive host selection menu
func runInteractiveMode() {
	configPath := Config.Paths.SSHConfig
	hosts := parseSSHConfig(configPath)

	if len(hosts) == 0 {
		fmt.Printf("No hosts found in %s\n", configPath)
		return
	}

	// Group filtering logic
	firstGroup := hosts[0].Group
	var rootHosts []HostConfig
	for _, h := range hosts {
		if h.Group == firstGroup {
			rootHosts = append(rootHosts, h)
		}
	}

	// Launch selection menu
	runHostSelectionMenu(rootHosts, hosts)
}
