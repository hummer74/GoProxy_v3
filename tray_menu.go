// tray_menu.go
package main

import (
    "fmt"
    "strings"
    "sync"
    "time"

    "github.com/getlantern/systray"
)

// Maximum number of hosts in a chain for pre-created display items
const maxChainDisplay = 10

// Visual separator for chain section (AddSeparator() returns void, can't Hide/Show)
const chainSeparator = "─────────────"

// Global variables for menu
var (
    hostMenuItems      map[string]*systray.MenuItem // Map for quick access to host menu items
    hostMenuItemsMutex sync.RWMutex
    killMenuItem       *systray.MenuItem
    exitMenuItem       *systray.MenuItem
    hostStatusCache    *HostStatusCache // Thread-safe host status cache
)

// Chain display section — pre-created items shown above main menu when chain is active
var (
    chainSectionItems     [maxChainDisplay]*systray.MenuItem
    chainSectionBottomSep *systray.MenuItem
)

// Chain builder state — ordered list of hosts selected by user for chain building
var (
    chainBuilder         []HostConfig // Ordered list: index = chain position
    chainBuilderMutex    sync.Mutex
    connectChainMenuItem *systray.MenuItem
    clearChainMenuItem   *systray.MenuItem
)

// HostStatusCache - thread-safe cache of host statuses
type HostStatusCache struct {
    mu     sync.RWMutex
    status map[string]bool
}

// NewHostStatusCache creates new status cache
func NewHostStatusCache() *HostStatusCache {
    return &HostStatusCache{
        status: make(map[string]bool),
    }
}

// Set sets host status
func (c *HostStatusCache) Set(hostName string, available bool) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.status[hostName] = available
}

// Get gets host status
func (c *HostStatusCache) Get(hostName string) (bool, bool) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    status, exists := c.status[hostName]
    return status, exists
}

// GetAll returns copy of all statuses
func (c *HostStatusCache) GetAll() map[string]bool {
    c.mu.RLock()
    defer c.mu.RUnlock()

    result := make(map[string]bool)
    for k, v := range c.status {
        result[k] = v
    }
    return result
}

// Clear clears cache
func (c *HostStatusCache) Clear() {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.status = make(map[string]bool)
}

// createTrayMenu creates the tray menu structure
func createTrayMenu() {
    defer func() {
        if r := recover(); r != nil {
            // Add error item to menu if creation fails
            errorItem := systray.AddMenuItem("Menu Error", "Failed to create menu")
            errorItem.Disable()
        }
    }()

    // Parse SSH config to get hosts
    hosts := parseSSHConfig(Config.Paths.SSHConfig)

    if len(hosts) == 0 {
        // Add a placeholder if no hosts found
        noHostsItem := systray.AddMenuItem("No hosts found", "Check SSH config")
        noHostsItem.Disable()
        systray.AddSeparator()
    } else {
        // Get first group hosts (like in interactive mode)
        firstGroup := hosts[0].Group
        var firstGroupHosts []HostConfig
        for _, h := range hosts {
            if h.Group == firstGroup {
                firstGroupHosts = append(firstGroupHosts, h)
            }
        }

        // Pre-create chain display section (hidden until chain is connected)
        for i := 0; i < maxChainDisplay; i++ {
            chainSectionItems[i] = systray.AddMenuItem("", "")
            chainSectionItems[i].Hide()
            chainSectionItems[i].Disable()
        }
        chainSectionBottomSep = systray.AddMenuItem(chainSeparator, "")
        chainSectionBottomSep.Disable()
        chainSectionBottomSep.Hide()

        // Check SSH connection for these hosts (async, we'll update as results come in)
        go updateHostStatusInMenu(firstGroupHosts, true)

        // Find max length for alignment
        maxLength := 0
        for _, host := range firstGroupHosts {
            if len(host.Name) > maxLength {
                maxLength = len(host.Name)
            }
        }

        // Add hosts from first group with alignment
        for _, host := range firstGroupHosts {
            // Create aligned title with initial "checking" status
            paddedName := padRight(host.Name, maxLength)
            menuTitle := fmt.Sprintf("○ %s", paddedName) // Empty circle for checking
            menuItem := systray.AddMenuItem(menuTitle, fmt.Sprintf("Checking SSH connection to %s...", host.Name))
            menuItem.Disable() // Initially disabled until we know status

            hostMenuItemsMutex.Lock()
            hostMenuItems[host.Name] = menuItem
            hostMenuItemsMutex.Unlock()

            // Set up click handler — toggle chain selection
            go func(h HostConfig, mi *systray.MenuItem) {
                for range mi.ClickedCh {
                    handleChainToggle(h)
                }
            }(host, menuItem)
        }

        // Add chain builder controls
        systray.AddSeparator()
        connectChainMenuItem = systray.AddMenuItem(fmt.Sprintf("%s Connect Chain", ChainArrow), "Select hosts then click to connect")
        connectChainMenuItem.Disable() // Disabled until at least 1 host is selected

        clearChainMenuItem = systray.AddMenuItem(fmt.Sprintf("%s Clear Selection", ClearX), "Clear all selected hosts from chain builder")
        clearChainMenuItem.Disable() // Disabled until at least 1 host is selected

        systray.AddSeparator()
    }

    // Add Kill Proxy item
    killMenuItem = systray.AddMenuItem("Stop Proxy", "Disable proxy and kill all tunnels")

    // Add Exit item
    exitMenuItem = systray.AddMenuItem("Exit", "Stop all proxy services and exit")

    // Set up click handlers for static items
    go func() {
        for range killMenuItem.ClickedCh {
            handleKillProxy()
        }
    }()

    go func() {
        for range exitMenuItem.ClickedCh {
            handleExit()
        }
    }()

    // Set up click handlers for chain builder buttons
    if connectChainMenuItem != nil {
        go func() {
            for range connectChainMenuItem.ClickedCh {
                handleConnectChain()
            }
        }()
    }
    if clearChainMenuItem != nil {
        go func() {
            for range clearChainMenuItem.ClickedCh {
                handleClearChain()
            }
        }()
    }

    // Update menu state based on current tunnel status
    updateMenuState()
}

// handleChainToggle toggles a host in/out of the chain builder
func handleChainToggle(host HostConfig) {
    // Check if host is available before adding
    if status, exists := hostStatusCache.Get(host.Name); exists && !status {
        return
    }

    // Check if this host is part of an active chain connection — ignore click
    if isTunnelActive && strings.Contains(currentHost, " -> ") {
        chainParts := strings.Split(currentHost, " -> ")
        for _, part := range chainParts {
            if part == host.Name {
                return
            }
        }
    }

    // Check if this is the currently active single host (ignore click)
    if isTunnelActive && !strings.Contains(currentHost, " -> ") && currentHost == host.Name {
        return
    }

    chainBuilderMutex.Lock()

    // Check if already in chain — remove it
    for i, h := range chainBuilder {
        if h.Name == host.Name {
            chainBuilder = append(chainBuilder[:i], chainBuilder[i+1:]...)
            logTunnelEvent("INFO", host.Name, fmt.Sprintf("Removed from chain builder (position %d)", i+1))
            chainBuilderMutex.Unlock() // Release before UI update to avoid deadlock
            updateChainBuilderUI()
            return
        }
    }

    // Not in chain — add to end
    chainBuilder = append(chainBuilder, host)
    logTunnelEvent("INFO", host.Name, fmt.Sprintf("Added to chain builder (position %d)", len(chainBuilder)))
    chainBuilderMutex.Unlock() // Release before UI update to avoid deadlock
    updateChainBuilderUI()
}

// getChainPosition returns the 1-based position of a host in the chain builder, or 0 if not present
func getChainPosition(hostName string) int {
    chainBuilderMutex.Lock()
    defer chainBuilderMutex.Unlock()

    for i, h := range chainBuilder {
        if h.Name == hostName {
            return i + 1
        }
    }
    return 0
}

// getChainBuilderCopy returns a thread-safe copy of the chain builder
func getChainBuilderCopy() []HostConfig {
    chainBuilderMutex.Lock()
    defer chainBuilderMutex.Unlock()

    result := make([]HostConfig, len(chainBuilder))
    copy(result, chainBuilder)
    return result
}

// getChainBuilderCount returns the number of hosts in the chain builder
func getChainBuilderCount() int {
    chainBuilderMutex.Lock()
    defer chainBuilderMutex.Unlock()
    return len(chainBuilder)
}

// clearChainBuilder clears the chain builder (thread-safe)
func clearChainBuilder() {
    chainBuilderMutex.Lock()
    defer chainBuilderMutex.Unlock()
    chainBuilder = nil
}

// updateChainBuilderUI updates all menu items to reflect the current chain builder state
func updateChainBuilderUI() {
    // Parse SSH config to get max length for alignment
    hosts := parseSSHConfig(Config.Paths.SSHConfig)
    if len(hosts) == 0 {
        return
    }

    firstGroup := hosts[0].Group
    var firstGroupHosts []HostConfig
    for _, h := range hosts {
        if h.Group == firstGroup {
            firstGroupHosts = append(firstGroupHosts, h)
        }
    }

    maxLength := 0
    for _, host := range firstGroupHosts {
        if len(host.Name) > maxLength {
            maxLength = len(host.Name)
        }
    }

    chainCount := getChainBuilderCount()
    chainCopy := getChainBuilderCopy()

    // Build a map of hostName -> chain position for quick lookup
    chainPositions := make(map[string]int)
    for i, h := range chainCopy {
        chainPositions[h.Name] = i + 1
    }

    // Build set of active chain hosts (if chain is connected)
    activeChainSet := make(map[string]bool)
    if isTunnelActive && strings.Contains(currentHost, " -> ") {
        for _, part := range strings.Split(currentHost, " -> ") {
            activeChainSet[part] = true
        }
    }

    hostMenuItemsMutex.RLock()
    defer hostMenuItemsMutex.RUnlock()

    // Update each host menu item
    for _, host := range firstGroupHosts {
        menuItem, exists := hostMenuItems[host.Name]
        if !exists || menuItem == nil {
            continue
        }

        paddedName := padRight(host.Name, maxLength)

        // Skip hosts that are part of an active chain — handled by updateMenuState
        if activeChainSet[host.Name] {
            menuItem.Hide()
            continue
        }

        // Make sure item is visible
        menuItem.Show()

        // Skip currently connected single host — handled by updateMenuState
        if isTunnelActive && !strings.Contains(currentHost, " -> ") && host.Name == currentHost {
            continue
        }

        if pos, inChain := chainPositions[host.Name]; inChain {
            // Host is in chain builder — show with [N] number
            posStr := fmt.Sprintf("[%d]", pos)
            menuItem.SetTitle(fmt.Sprintf("%s %s", posStr, paddedName))

            // Build tooltip showing chain order
            var chainNames []string
            for i, ch := range chainCopy {
                chainNames = append(chainNames, fmt.Sprintf("[%d] %s", i+1, ch.Name))
            }
            chainDisplay := strings.Join(chainNames, " -> ")
            menuItem.SetTooltip(fmt.Sprintf("Chain: %s", chainDisplay))
            menuItem.Enable()
        } else {
            // Host is not in chain — show normal status from cache
            if status, ok := hostStatusCache.Get(host.Name); ok {
                if status {
                    menuItem.SetTitle(fmt.Sprintf("%s %s", GreenCircle, paddedName))
                    menuItem.SetTooltip(fmt.Sprintf("%s: SSH connection available", host.Name))
                    menuItem.Enable()
                } else {
                    menuItem.SetTitle(fmt.Sprintf("%s %s", RedCircle, paddedName))
                    menuItem.SetTooltip(fmt.Sprintf("%s: SSH connection failed", host.Name))
                    menuItem.Disable()
                }
            }
        }
    }

    // Update Connect Chain button
    if connectChainMenuItem != nil {
        if chainCount >= 1 {
            if chainCount == 1 {
                connectChainMenuItem.SetTitle(fmt.Sprintf("%s Connect (%d host)", ChainArrow, chainCount))
            } else {
                connectChainMenuItem.SetTitle(fmt.Sprintf("%s Connect Chain (%d hosts)", ChainArrow, chainCount))
            }

            // Build tooltip with chain order
            var chainNames []string
            for i, ch := range chainCopy {
                chainNames = append(chainNames, fmt.Sprintf("[%d] %s", i+1, ch.Name))
            }
            chainDisplay := strings.Join(chainNames, " -> ")
            connectChainMenuItem.SetTooltip(fmt.Sprintf("Connect: %s", chainDisplay))
            connectChainMenuItem.Enable()
        } else {
            connectChainMenuItem.SetTitle(fmt.Sprintf("%s Connect Chain", ChainArrow))
            connectChainMenuItem.SetTooltip("Select hosts then click to connect")
            connectChainMenuItem.Disable()
        }
    }

    // Update Clear Selection button
    if clearChainMenuItem != nil {
        if chainCount >= 1 {
            clearChainMenuItem.Enable()
        } else {
            clearChainMenuItem.Disable()
        }
    }
}

// updateHostStatusInMenu checks host status and updates menu
// initialCheck is true when called from createTrayMenu (shows "checking" status)
func updateHostStatusInMenu(hosts []HostConfig, initialCheck bool) {
    // Give time for menu rendering
    time.Sleep(100 * time.Millisecond)

    // Check host statuses
    sshStatusCache := checkSSHConnectionBatch(hosts, Config.Paths.WorkDir)

    // Find max length for alignment
    maxLength := 0
    for _, host := range hosts {
        if len(host.Name) > maxLength {
            maxLength = len(host.Name)
        }
    }

    // Build set of active chain hosts (if chain is connected)
    activeChainSet := make(map[string]bool)
    if isTunnelActive && strings.Contains(currentHost, " -> ") {
        for _, part := range strings.Split(currentHost, " -> ") {
            activeChainSet[part] = true
        }
    }

    // Update each menu item
    for _, host := range hosts {
        hostMenuItemsMutex.RLock()
        menuItem, exists := hostMenuItems[host.Name]
        hostMenuItemsMutex.RUnlock()

        if !exists || menuItem == nil {
            continue
        }

        paddedName := padRight(host.Name, maxLength)

        // Skip updating if this host is currently connected (it will be handled by updateMenuState)
        if isTunnelActive {
            if strings.Contains(currentHost, " -> ") {
                // Current connection is a chain, skip all chain hosts
                if activeChainSet[host.Name] {
                    hostStatusCache.Set(host.Name, true)
                    continue
                }
            } else if host.Name == currentHost {
                // This is the currently connected single host
                hostStatusCache.Set(host.Name, true)
                continue
            }
        }

        // Update status cache
        hostStatusCache.Set(host.Name, sshStatusCache[host.Name])

        // Skip hosts that are in chain builder — they have their own display
        if getChainPosition(host.Name) > 0 {
            continue
        }

        if sshStatusCache[host.Name] {
            // Host is available - green circle and enable menu item
            menuItem.SetTitle(fmt.Sprintf("%s %s", GreenCircle, paddedName))
            menuItem.SetTooltip(fmt.Sprintf("%s: SSH connection available", host.Name))
            menuItem.Enable()
        } else {
            // Host is unavailable - red circle and disable menu item
            menuItem.SetTitle(fmt.Sprintf("%s %s", RedCircle, paddedName))
            menuItem.SetTooltip(fmt.Sprintf("%s: SSH connection failed", host.Name))
            menuItem.Disable()
        }
    }
}

// updateMenuState updates the state of menu items based on current tunnel status
func updateMenuState() {
    // Parse SSH config to get max length for alignment
    hosts := parseSSHConfig(Config.Paths.SSHConfig)
    if len(hosts) == 0 {
        return
    }

    // Get first group hosts
    firstGroup := hosts[0].Group
    var firstGroupHosts []HostConfig
    for _, h := range hosts {
        if h.Group == firstGroup {
            firstGroupHosts = append(firstGroupHosts, h)
        }
    }

    // Find max length for alignment
    maxLength := 0
    for _, host := range firstGroupHosts {
        if len(host.Name) > maxLength {
            maxLength = len(host.Name)
        }
    }

    // Parse active chain connection
    isChain := false
    var chainParts []string
    chainHostSet := make(map[string]bool)
    lastChainHost := ""
    if isTunnelActive && currentHost != "" {
        if strings.Contains(currentHost, " -> ") {
            isChain = true
            chainParts = strings.Split(currentHost, " -> ")
            for _, h := range chainParts {
                chainHostSet[h] = true
            }
            if len(chainParts) > 0 {
                lastChainHost = chainParts[len(chainParts)-1]
            }
        }
    }

    // --- Chain section display (above main menu) ---
    if isChain {
        chainSectionBottomSep.Show()

        // Find max name length in chain for padding
        chainMaxLength := 0
        for _, h := range chainParts {
            if len(h) > chainMaxLength {
                chainMaxLength = len(h)
            }
        }

        for i := 0; i < maxChainDisplay; i++ {
            if i < len(chainParts) {
                hostName := chainParts[i]
                pos := i + 1
                posStr := fmt.Sprintf("[%d]", pos)
                paddedName := padRight(hostName, chainMaxLength)
                item := chainSectionItems[i]

                if hostName == lastChainHost {
                    // Exit host: ✓ ● [N] alias (HH:MM)
                    item.Check()
                    item.Enable()
                    if !tunnelStartTime.IsZero() {
                        duration := time.Since(tunnelStartTime)
                        durationStr := formatDuration(duration)
                        item.SetTitle(fmt.Sprintf("%s %s %s (%s)", GreenCircle, posStr, paddedName, durationStr))
                        item.SetTooltip(fmt.Sprintf("Chain exit: %s\nConnected for: %s", currentHost, durationStr))
                    } else {
                        item.SetTitle(fmt.Sprintf("%s %s %s", GreenCircle, posStr, paddedName))
                        item.SetTooltip(fmt.Sprintf("Chain exit: %s", currentHost))
                    }
                } else if pos == 1 {
                    // Entry host: ✓ ● [1] alias
                    item.Check()
                    item.Enable()
                    item.SetTitle(fmt.Sprintf("%s %s %s", RedCircle, posStr, paddedName))
                    item.SetTooltip(fmt.Sprintf("Chain entry → %s", currentHost))
                } else {
                    // Intermediate host: ☐ → [N] alias
                    item.Uncheck()
                    item.Enable()
                    item.SetTitle(fmt.Sprintf("%s %s %s", CurrentArrow, posStr, paddedName))
                    item.SetTooltip(fmt.Sprintf("Chain hop %d → %s", pos, currentHost))
                }
                item.Show()
            } else {
                chainSectionItems[i].Hide()
            }
        }
    } else {
        // Hide entire chain section
        chainSectionBottomSep.Hide()
        for i := 0; i < maxChainDisplay; i++ {
            chainSectionItems[i].Hide()
        }
    }

    // --- Main menu items ---
    hostMenuItemsMutex.RLock()
    defer hostMenuItemsMutex.RUnlock()

    // Get current chain builder state
    chainCopy := getChainBuilderCopy()
    chainBuilderPos := make(map[string]int)
    for i, h := range chainCopy {
        chainBuilderPos[h.Name] = i + 1
    }

    // Update each host menu item
    for hostName, menuItem := range hostMenuItems {
        if menuItem == nil {
            continue
        }

        paddedName := padRight(hostName, maxLength)

        // If chain is active, hide chain hosts from main menu
        if isChain && chainHostSet[hostName] {
            menuItem.Hide()
            continue
        }

        // Make sure item is visible
        menuItem.Show()

        if !isChain && isTunnelActive && hostName == currentHost {
            // Single host connection
            menuItem.Check()
            if !tunnelStartTime.IsZero() {
                duration := time.Since(tunnelStartTime)
                durationStr := formatDuration(duration)
                menuItem.SetTitle(fmt.Sprintf("%s %s (%s)", CurrentArrow, padRight(hostName, maxLength), durationStr))
                menuItem.SetTooltip(fmt.Sprintf("Currently connected to %s for %s", hostName, durationStr))
            } else {
                menuItem.SetTitle(fmt.Sprintf("%s %s", CurrentArrow, padRight(hostName, maxLength)))
                menuItem.SetTooltip(fmt.Sprintf("Currently connected to %s", hostName))
            }
            menuItem.Enable()
        } else if pos, inChain := chainBuilderPos[hostName]; inChain {
            // Host is in chain builder — show with [N] number
            posStr := fmt.Sprintf("[%d]", pos)
            menuItem.Uncheck()
            menuItem.SetTitle(fmt.Sprintf("%s %s", posStr, paddedName))

            // Build tooltip showing chain order
            var chainNames []string
            for i, ch := range chainCopy {
                chainNames = append(chainNames, fmt.Sprintf("[%d] %s", i+1, ch.Name))
            }
            chainDisplay := strings.Join(chainNames, " -> ")
            menuItem.SetTooltip(fmt.Sprintf("Chain: %s", chainDisplay))
            menuItem.Enable()
        } else {
            // Not active host and not in chain
            menuItem.Uncheck()

            // Restore status from cache if available
            if status, exists := hostStatusCache.Get(hostName); exists {
                var statusIcon, tooltip string

                if status {
                    statusIcon = GreenCircle
                    tooltip = fmt.Sprintf("%s: SSH connection available", hostName)
                    menuItem.Enable()
                } else {
                    statusIcon = RedCircle
                    tooltip = fmt.Sprintf("%s: SSH connection failed", hostName)
                    menuItem.Disable()
                }

                menuItem.SetTitle(fmt.Sprintf("%s %s", statusIcon, paddedName))
                menuItem.SetTooltip(tooltip)
            }
        }
    }
}

// menuUpdateLoop periodically updates the menu (only UI, not monitoring)
func menuUpdateLoop() {
    if menuUpdateTicker != nil {
        menuUpdateTicker.Stop()
    }
    menuUpdateTicker = time.NewTicker(1 * time.Second)

    for range menuUpdateTicker.C {
        // Update menu if tunnel is active (to update duration)
        if isTunnelActive {
            updateMenuState()
        }
    }
}

// padRight pads string with spaces to the right to achieve alignment
func padRight(s string, length int) string {
    if len(s) >= length {
        return s
    }
    return s + strings.Repeat(" ", length-len(s))
}

// formatDuration formats duration as HH:MM (24-hour format)
func formatDuration(d time.Duration) string {
    totalMinutes := int(d.Minutes())
    hours := (totalMinutes / 60) % 24
    minutes := totalMinutes % 60
    return fmt.Sprintf("%02d:%02d", hours, minutes)
}