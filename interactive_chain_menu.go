// interactive_chain_menu.go
package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/eiannone/keyboard"
)

var (
	statusCache map[string]string
	statusMutex sync.Mutex
)

func runHostSelectionMenu(hosts, allHostsForTransition []HostConfig) {
	workDir := Config.Paths.WorkDir
	lastHostCfgPath := Config.Paths.LastHostFile

	statusMutex.Lock()
	statusCache = make(map[string]string)
	for _, h := range hosts {
		statusCache[h.Name] = "Checking..."
	}
	statusMutex.Unlock()

	go func() {
		results := checkSSHConnectionBatch(hosts, workDir)
		statusMutex.Lock()
		for name, ok := range results {
			if ok {
				statusCache[name] = "Online"
			} else {
				statusCache[name] = "Offline"
			}
		}
		statusMutex.Unlock()
	}()

	var chainedHosts []HostConfig
	lastHostContent, _ := os.ReadFile(lastHostCfgPath)
	lastData := strings.TrimSpace(string(lastHostContent))

	selectedIndex := 0

	if lastData != "" {
		parts := strings.Split(lastData, "|")
		// Restore the chain
		for _, p := range parts {
			for _, h := range hosts {
				if h.Name == p {
					chainedHosts = append(chainedHosts, h)
					break
				}
			}
		}
		// Set cursor to the LAST host in the chain/last host used
		if len(parts) > 0 {
			targetName := parts[len(parts)-1]
			for i, h := range hosts {
				if h.Name == targetName {
					selectedIndex = i
					break
				}
			}
		}
	}

	if err := keyboard.Open(); err != nil {
		fmt.Printf("Failed to open keyboard: %v\n", err)
		return
	}
	defer keyboard.Close()

	// --- AUTO-CONNECT LOGIC ---
	var initialChar rune
	var initialKey keyboard.Key
	if Config.General.AutoConnect && lastData != "" && len(chainedHosts) > 0 {
		timeout := Config.General.AutoSelectTimeout
		cancelChan := make(chan struct {
			char rune
			key  keyboard.Key
		}, 1)

		go func() {
			c, k, err := keyboard.GetKey()
			if err == nil {
				cancelChan <- struct {
					char rune
					key  keyboard.Key
				}{char: c, key: k}
			}
		}()

		autoConnectTriggered := true
		for i := timeout; i > 0; i-- {
			renderSelectionMenu(hosts, selectedIndex, chainedHosts)
			fmt.Printf("\n%s>>> Auto-connecting in %d seconds... (Press any key to cancel)%s\n", ColorYellow, i, ColorReset)

			select {
			case ev := <-cancelChan:
				autoConnectTriggered = false
				initialChar = ev.char
				initialKey = ev.key
				fmt.Printf("\n%s[!] Auto-connect cancelled by user.%s\n", ColorCyan, ColorReset)
				time.Sleep(1 * time.Second)
				goto manualMode
			case <-time.After(1 * time.Second):
				continue
			}
		}

		if autoConnectTriggered {
			if len(chainedHosts) > 1 {
				handleChainConnectionInteractive(chainedHosts)
			} else {
				handleConnectionInteractive(chainedHosts[0])
			}
			return
		}
	}

manualMode:
	if initialChar != 0 || initialKey != 0 {
		if processKeyAction(initialChar, initialKey, hosts, &selectedIndex, &chainedHosts) {
			return
		}
		initialChar = 0
		initialKey = 0
	}

	for {
		renderSelectionMenu(hosts, selectedIndex, chainedHosts)

		char, key, err := keyboard.GetKey()
		if err != nil {
			break
		}

		if processKeyAction(char, key, hosts, &selectedIndex, &chainedHosts) {
			return
		}
	}
}

func renderSelectionMenu(hosts []HostConfig, selectedIndex int, chainedHosts []HostConfig) {
	fmt.Print("\033[H\033[2J")
	fmt.Println("=== GoProxy - Host Selection ===")
	fmt.Println("--------------------------------------------------------------------------")

	for i, host := range hosts {
		prefix := "  "
		if i == selectedIndex {
			prefix = "> "
		}

		inChain := ""
		for pos, ch := range chainedHosts {
			if ch.Name == host.Name {
				inChain = fmt.Sprintf(" \033[33m[#%d]\033[0m", pos+1)
				break
			}
		}

		status := "[...]"
		statusMutex.Lock()
		if s, ok := statusCache[host.Name]; ok {
			if s == "Online" {
				status = "[\033[32mON\033[0m]"
			} else if s == "Offline" {
				status = "[\033[31mOFF\033[0m]"
			}
		}
		statusMutex.Unlock()

		fmt.Printf("%s%s %-15s | %-15s%s\n", prefix, status, host.Name, host.HostName, inChain)
	}

	fmt.Println("--------------------------------------------------------------------------")
	fmt.Println("UP/DOWN: Navigate | SPACE: Add/Remove from Chain | ENTER: Connect | ESC: Exit")
}

func isSpaceAction(char rune, key keyboard.Key) bool {
	return char == ' ' || key == keyboard.KeySpace || key == keyboard.Key(' ')
}

func toggleHostChain(hosts []HostConfig, selectedIndex int, chainedHosts *[]HostConfig) {
	current := hosts[selectedIndex]
	foundIdx := -1
	for i, ch := range *chainedHosts {
		if ch.Name == current.Name {
			foundIdx = i
			break
		}
	}
	if foundIdx != -1 {
		*chainedHosts = append((*chainedHosts)[:foundIdx], (*chainedHosts)[foundIdx+1:]...)
	} else {
		*chainedHosts = append(*chainedHosts, current)
	}
}

func processKeyAction(char rune, key keyboard.Key, hosts []HostConfig, selectedIndex *int, chainedHosts *[]HostConfig) bool {
	if key == keyboard.KeyArrowUp {
		if *selectedIndex > 0 {
			*selectedIndex--
		}
		return false
	}

	if key == keyboard.KeyArrowDown {
		if *selectedIndex < len(hosts)-1 {
			*selectedIndex++
		}
		return false
	}

	if isSpaceAction(char, key) {
		toggleHostChain(hosts, *selectedIndex, chainedHosts)
		return false
	}

	if key == keyboard.KeyEsc || key == keyboard.KeyCtrlC {
		return true
	}

	if key == keyboard.KeyEnter {
		if len(*chainedHosts) > 0 {
			handleChainConnectionInteractive(*chainedHosts)
			return true
		}
		handleConnectionInteractive(hosts[*selectedIndex])
		return true
	}

	return false
}
