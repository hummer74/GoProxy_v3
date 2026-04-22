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
        allMenuHosts       []HostConfig     // ALL hosts from all groups (preserved for alignment & lookups)
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

// ── Helpers ──────────────────────────────────────────────────────────────

// hostGroup holds a group name and its hosts.
type hostGroup struct {
        Name  string
        Hosts []HostConfig
}

// hostsByGroup groups hosts by their Group field, preserving insertion order.
func hostsByGroup(hosts []HostConfig) []hostGroup {
        var groups []hostGroup
        seen := make(map[string]int)

        for _, h := range hosts {
                if idx, exists := seen[h.Group]; exists {
                        groups[idx].Hosts = append(groups[idx].Hosts, h)
                } else {
                        seen[h.Group] = len(groups)
                        groups = append(groups, hostGroup{Name: h.Group, Hosts: []HostConfig{h}})
                }
        }
        return groups
}

// isReverseHost returns true if the host has a ProxyJump directive.
func isReverseHost(h HostConfig) bool {
        return h.ProxyJump != ""
}

// isReverseJumperHost returns true if the host belongs to the REVERSE JUMPER group.
// These hosts are used only for building reverse chains (ProxyJump targets)
// and must never be used as failover candidates.
func isReverseJumperHost(h HostConfig) bool {
        return strings.Contains(h.Group, "REVERSE JUMPER")
}

// getGlobalMaxLength returns the maximum host name length across all menu hosts.
func getGlobalMaxLength() int {
        maxLen := 0
        hostMenuItemsMutex.RLock()
        defer hostMenuItemsMutex.RUnlock()
        for name := range hostMenuItems {
                if len(name) > maxLen {
                        maxLen = len(name)
                }
        }
        return maxLen
}

// ── Menu creation ────────────────────────────────────────────────────────

// createTrayMenu creates the tray menu structure with ALL host groups.
func createTrayMenu() {
        debugLog("MENU", "createTrayMenu called, allMenuHosts count=%d", len(allMenuHosts))
        defer func() {
                if r := recover(); r != nil {
                        debugLog("MENU", "PANIC in createTrayMenu: %v", r)
                        writeCrashLog(r)
                        // Add error item to menu if creation fails
                        errorItem := systray.AddMenuItem("Menu Error", fmt.Sprintf("Failed to create menu: %v", r))
                        errorItem.Disable()
                }
        }()

        // Parse SSH config to get hosts from ALL groups
        debugLog("MENU", "Parsing SSH config: %s", Config.Paths.SSHConfig)
        hosts := parseSSHConfig(Config.Paths.SSHConfig)

        debugLog("MENU", "Parsed %d hosts from SSH config", len(hosts))
        if len(hosts) > 0 {
                for i, h := range hosts {
                        debugLog("MENU", "  Host[%d]: name=%q group=%q host=%q port=%s proxyJump=%q", i, h.Name, h.Group, h.HostName, h.Port, h.ProxyJump)
                }
        }

        if len(hosts) == 0 {
                // Add a placeholder if no hosts found
                noHostsItem := systray.AddMenuItem("No hosts found", "Check SSH config")
                noHostsItem.Disable()
                systray.AddSeparator()
        } else {
                // Store all hosts globally for later use (alignment, lookups)
                allMenuHosts = hosts

                debugLog("MENU", "Pre-creating chain display section")

                // Pre-create chain display section (hidden until chain is connected)
                for i := 0; i < maxChainDisplay; i++ {
                        chainSectionItems[i] = systray.AddMenuItem("", "")
                        chainSectionItems[i].Hide()
                        chainSectionItems[i].Disable()
                        debugLog("MENU", "  chainSectionItems[%d] created", i)
                }
                chainSectionBottomSep = systray.AddMenuItem(chainSeparator, "")
                chainSectionBottomSep.Disable()
                chainSectionBottomSep.Hide()
                debugLog("MENU", "chainSectionBottomSep created")

                // Find global max length for alignment across all groups
                maxLength := 0
                for _, host := range hosts {
                        if len(host.Name) > maxLength {
                                maxLength = len(host.Name)
                        }
                }
                debugLog("MENU", "Global max name length: %d", maxLength)

                // Group hosts by their Group field
                groups := hostsByGroup(hosts)
                debugLog("MENU", "Groups: %d", len(groups))

                // Add hosts from each group
                for gi, group := range groups {
                        debugLog("MENU", "Processing group %d: %q (%d hosts)", gi, group.Name, len(group.Hosts))

                        // Add group separator for non-first groups
                        if gi > 0 {
                                systray.AddSeparator()
                                groupHeader := systray.AddMenuItem(fmt.Sprintf("── %s ──", group.Name), "")
                                groupHeader.Disable()
                                debugLog("MENU", "  Group separator added: %q", group.Name)
                        }

                        // Check SSH connection for hosts in this group (async)
                        // Capture group.Hosts to avoid closure issue with loop variable
                        groupHosts := make([]HostConfig, len(group.Hosts))
                        copy(groupHosts, group.Hosts)
                        safeGo(func() {
                                debugLog("MENU", "Background status check starting for %d hosts", len(groupHosts))
                                updateHostStatusInMenu(groupHosts, true)
                                debugLog("MENU", "Background status check completed for %d hosts", len(groupHosts))
                        })

                        // Add host items (direct first, then reverse with separator)
                        for hi, host := range group.Hosts {
                                debugLog("MENU", "  Adding menu item[%d]: %q (group=%q)", hi, host.Name, host.Group)

                                // Add separator before first reverse host in group
                                if isReverseHost(host) && (hi == 0 || !isReverseHost(group.Hosts[hi-1])) {
                                        systray.AddSeparator()
                                        debugLog("MENU", "    Separator before reverse hosts")
                                }

                                paddedName := padRight(host.Name, maxLength)
                                menuTitle := fmt.Sprintf("○ %s", paddedName) // Empty circle for checking

                                tooltip := fmt.Sprintf("Checking SSH connection to %s...", host.Name)
                                if isReverseHost(host) {
                                        debugLog("MENU", "    → Reverse host (ProxyJump=%q)", host.ProxyJump)
                                } else {
                                        debugLog("MENU", "    → Direct host")
                                }

                                menuItem := systray.AddMenuItem(menuTitle, tooltip)
                                menuItem.Disable() // Initially disabled until we know status
                                debugLog("MENU", "    Menu item created and disabled")

                                hostMenuItemsMutex.Lock()
                                hostMenuItems[host.Name] = menuItem
                                hostMenuItemsMutex.Unlock()
                                debugLog("MENU", "    Stored in hostMenuItems map")

                                if isReverseHost(host) {
                                        // Reverse host: click → immediate connect (auto-resolve ProxyJump)
                                        h, mi := host, menuItem
                                        safeGo(func() {
                                                for range mi.ClickedCh {
                                                        handleReverseHostClick(h)
                                                }
                                        })
                                } else {
                                        // Direct host: click → toggle chain builder
                                        h, mi := host, menuItem
                                        safeGo(func() {
                                                for range mi.ClickedCh {
                                                        handleChainToggle(h)
                                                }
                                        })
                                }
                        }
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
        debugLog("MENU", "Kill Proxy item added")

        // Add Exit item
        exitMenuItem = systray.AddMenuItem("Exit", "Stop all proxy services and exit")
        debugLog("MENU", "Exit item added")

        // Set up click handlers for static items
        safeGo(func() {
                for range killMenuItem.ClickedCh {
                        handleKillProxy()
                }
        })

        safeGo(func() {
                for range exitMenuItem.ClickedCh {
                        handleExit()
                }
        })

        // Set up click handlers for chain builder buttons
        if connectChainMenuItem != nil {
                safeGo(func() {
                        for range connectChainMenuItem.ClickedCh {
                                handleConnectChain()
                        }
                })
        }
        if clearChainMenuItem != nil {
                safeGo(func() {
                        for range clearChainMenuItem.ClickedCh {
                                handleClearChain()
                        }
                })
        }

        // Update menu state based on current tunnel status
        updateMenuState()
}

// ── Chain builder ────────────────────────────────────────────────────────

// handleChainToggle toggles a host in/out of the chain builder
func handleChainToggle(host HostConfig) {
        debugLog("MENU", "Chain toggle: %s", host.Name)
        // Check if host is available before adding
        if status, exists := hostStatusCache.Get(host.Name); exists && !status {
                return
        }

        // Check if this host is part of an active chain connection — ignore click
        currentHostVal := connState.GetHost()
        if connState.IsActive() && strings.Contains(currentHostVal, " -> ") {
                chainParts := strings.Split(currentHostVal, " -> ")
                for _, part := range chainParts {
                        if part == host.Name {
                                return
                        }
                }
        }

        // Check if this is the currently active single host (ignore click)
        if connState.IsActive() && !strings.Contains(currentHostVal, " -> ") && currentHostVal == host.Name {
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

// updateChainBuilderUI updates all menu items to reflect the current chain builder state.
// Iterates over ALL hostMenuItems (all groups) instead of just the first group.
func updateChainBuilderUI() {
        defer func() {
                if r := recover(); r != nil {
                        debugLog("MENU", "PANIC in updateChainBuilderUI: %v", r)
                        writeCrashLog(r)
                }
        }()
        maxLength := getGlobalMaxLength()

        chainCount := getChainBuilderCount()
        chainCopy := getChainBuilderCopy()

        // Build a map of hostName -> chain position for quick lookup
        chainPositions := make(map[string]int)
        for i, h := range chainCopy {
                chainPositions[h.Name] = i + 1
        }

        // Build set of active chain hosts (if chain is connected)
        activeChainSet := make(map[string]bool)
        currentHostVal := connState.GetHost()
        if connState.IsActive() && strings.Contains(currentHostVal, " -> ") {
                for _, part := range strings.Split(currentHostVal, " -> ") {
                        activeChainSet[part] = true
                }
        }

        hostMenuItemsMutex.RLock()
        defer hostMenuItemsMutex.RUnlock()

        // Update each host menu item (all groups)
        for hostName, menuItem := range hostMenuItems {
                if menuItem == nil {
                        continue
                }

                paddedName := padRight(hostName, maxLength)

                // Skip hosts that are part of an active chain — handled by updateMenuState
                if activeChainSet[hostName] {
                        menuItem.Hide()
                        continue
                }

                // Make sure item is visible
                menuItem.Show()

                // Skip currently connected single host — handled by updateMenuState
                if connState.IsActive() && !strings.Contains(currentHostVal, " -> ") && hostName == currentHostVal {
                        continue
                }

                // Skip reverse hosts — they don't participate in chain builder
                if hc := findHostByName(allMenuHosts, hostName); hc != nil && isReverseHost(*hc) {
                        continue
                }

                if pos, inChain := chainPositions[hostName]; inChain {
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
                        if status, ok := hostStatusCache.Get(hostName); ok {
                                if status {
                                        menuItem.SetTitle(fmt.Sprintf("%s %s", GreenCircle, paddedName))
                                        menuItem.SetTooltip(fmt.Sprintf("%s: SSH connection available", hostName))
                                        menuItem.Enable()
                                } else {
                                        menuItem.SetTitle(fmt.Sprintf("%s %s", RedCircle, paddedName))
                                        menuItem.SetTooltip(fmt.Sprintf("%s: SSH connection failed", hostName))
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

// ── Host status checking ─────────────────────────────────────────────────

// updateHostStatusInMenu checks host status and updates menu items.
// For reverse hosts (with ProxyJump), checks the ProxyJump target instead,
// since reverse hosts connect to 127.0.0.1:PORT which isn't directly reachable.
// initialCheck is true when called from createTrayMenu (shows "checking" status).
func updateHostStatusInMenu(hosts []HostConfig, initialCheck bool) {
        defer func() {
                if r := recover(); r != nil {
                        debugLog("MENU", "PANIC in updateHostStatusInMenu: %v", r)
                        writeCrashLog(r)
                }
        }()
        debugLog("MENU", "Checking status for %d hosts (initial=%v)", len(hosts), initialCheck)
        // Give time for menu rendering
        time.Sleep(100 * time.Millisecond)

        // Defensive: if hostStatusCache is nil, skip
        if hostStatusCache == nil {
                debugLog("MENU", "hostStatusCache is nil, skipping status update")
                return
        }

        // Build a lookup of all parsed hosts (for resolving ProxyJump targets)
        parsedMap := make(map[string]HostConfig)
        for _, h := range allMenuHosts {
                parsedMap[h.Name] = h
        }

        // For each host, determine the effective host to check.
        // Reverse hosts → check their ProxyJump target; direct hosts → check themselves.
        type checkEntry struct {
                effectiveHost HostConfig
                origNames     []string // all original host names that map to this check
        }
        effectiveMap := make(map[string]*checkEntry) // key = effective host name

        for _, h := range hosts {
                effectiveHost := h
                if isReverseHost(h) {
                        if target, ok := parsedMap[h.ProxyJump]; ok {
                                effectiveHost = target
                        }
                        // If ProxyJump target not found, fall back to checking the host directly
                }

                if entry, exists := effectiveMap[effectiveHost.Name]; exists {
                        entry.origNames = append(entry.origNames, h.Name)
                } else {
                        effectiveMap[effectiveHost.Name] = &checkEntry{
                                effectiveHost: effectiveHost,
                                origNames:     []string{h.Name},
                        }
                }
        }

        // Build deduplicated effective hosts list
        var effectiveHosts []HostConfig
        for _, entry := range effectiveMap {
                effectiveHosts = append(effectiveHosts, entry.effectiveHost)
        }

        // Check effective hosts
        sshStatusCache := checkSSHConnectionBatch(effectiveHosts, Config.Paths.WorkDir)

        debugLog("MENU", "SSH status check results: %v", sshStatusCache)

        // Map results back to original host names
        resultMap := make(map[string]bool)
        for effectiveName, status := range sshStatusCache {
                entry := effectiveMap[effectiveName]
                if entry == nil {
                        continue
                }
                for _, origName := range entry.origNames {
                        resultMap[origName] = status
                }
        }

        // Find max length for alignment
        maxLength := getGlobalMaxLength()

        // Build set of active chain hosts (if chain is connected)
        activeChainSet := make(map[string]bool)
        currentHostVal := connState.GetHost()
        if connState.IsActive() && strings.Contains(currentHostVal, " -> ") {
                for _, part := range strings.Split(currentHostVal, " -> ") {
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

                // Skip updating if this host is currently connected (handled by updateMenuState)
                if connState.IsActive() {
                        if strings.Contains(currentHostVal, " -> ") {
                                if activeChainSet[host.Name] {
                                        hostStatusCache.Set(host.Name, true)
                                        continue
                                }
                        } else if host.Name == currentHostVal {
                                hostStatusCache.Set(host.Name, true)
                                continue
                        }
                }

                // Update status cache with mapped result
                hostStatusCache.Set(host.Name, resultMap[host.Name])

                // Skip hosts that are in chain builder — they have their own display
                if getChainPosition(host.Name) > 0 {
                        continue
                }

                tooltip := fmt.Sprintf("%s: %s", host.Name, statusText(resultMap[host.Name]))

                if resultMap[host.Name] {
                        menuItem.SetTitle(fmt.Sprintf("%s %s", GreenCircle, paddedName))
                        menuItem.SetTooltip(tooltip)
                        menuItem.Enable()
                } else {
                        menuItem.SetTitle(fmt.Sprintf("%s %s", RedCircle, paddedName))
                        menuItem.SetTooltip(tooltip)
                        menuItem.Disable()
                }
        }
}

// statusText returns a human-readable status string.
func statusText(available bool) string {
        if available {
                return "SSH connection available"
        }
        return "SSH connection failed"
}

// ── Menu state ───────────────────────────────────────────────────────────

// lastMenuState tracks the previous state for change detection
var lastMenuState struct {
        active bool
        host   string
}

// updateMenuState updates the state of menu items based on current tunnel status.
// Iterates over ALL hostMenuItems (all groups) instead of just the first group.
func updateMenuState() {
        defer func() {
                if r := recover(); r != nil {
                        debugLog("MENU", "PANIC in updateMenuState: %v", r)
                        writeCrashLog(r)
                }
        }()
        maxLength := getGlobalMaxLength()

        // Parse active chain connection
        currentHostVal := connState.GetHost()

        // Log only when state actually changes
        isActive := connState.IsActive()
        if isActive != lastMenuState.active || currentHostVal != lastMenuState.host {
                debugLog("MENU", "updateMenuState: active=%v, host=%q", isActive, currentHostVal)
                lastMenuState.active = isActive
                lastMenuState.host = currentHostVal
        }

        isChain := false
        var chainParts []string
        chainHostSet := make(map[string]bool)
        if connState.IsActive() && currentHostVal != "" {
                if strings.Contains(currentHostVal, " -> ") {
                        isChain = true
                        chainParts = strings.Split(currentHostVal, " -> ")
                        for _, h := range chainParts {
                                chainHostSet[h] = true
                        }
                }
        }

        // --- Chain section display (above main menu) ---
        if isChain && chainSectionBottomSep != nil {
                chainSectionBottomSep.Show()

                // Check if this is a reverse connection (2 hops, last has ProxyJump=first)
                displayChainParts := chainParts
                if len(chainParts) == 2 {
                        lastHost := chainParts[len(chainParts)-1]
                        if hc := findHostByName(allMenuHosts, lastHost); hc != nil && hc.ProxyJump == chainParts[0] {
                                displayChainParts = chainParts[1:2] // Show only the target host
                        }
                }

                // Find max name length for padding
                chainMaxLength := 0
                for _, h := range displayChainParts {
                        if len(h) > chainMaxLength {
                                chainMaxLength = len(h)
                        }
                }

                for i := 0; i < maxChainDisplay; i++ {
                        item := chainSectionItems[i]
                        if item == nil {
                                continue
                        }
                        if i < len(displayChainParts) {
                                hostName := displayChainParts[i]
                                paddedName := padRight(hostName, chainMaxLength)

                                item.Check()
                                item.Enable()
                                startedTime := connState.GetStartTime()
                                if !startedTime.IsZero() {
                                        duration := time.Since(startedTime)
                                        durationStr := formatDuration(duration)
                                        item.SetTitle(fmt.Sprintf("%s (%s)", paddedName, durationStr))
                                        item.SetTooltip(fmt.Sprintf("Connected: %s\nDuration: %s", currentHostVal, durationStr))
                                } else {
                                        item.SetTitle(fmt.Sprintf("%s", paddedName))
                                        item.SetTooltip(fmt.Sprintf("Connected: %s", currentHostVal))
                                }
                                item.Show()
                        } else {
                                item.Hide()
                        }
                }
        } else {
                // Hide entire chain section
                if chainSectionBottomSep != nil {
                        chainSectionBottomSep.Hide()
                }
                for i := 0; i < maxChainDisplay; i++ {
                        if chainSectionItems[i] != nil {
                                chainSectionItems[i].Hide()
                        }
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

        // Update each host menu item (all groups)
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

                if !isChain && connState.IsActive() && hostName == currentHostVal {
                        // Single host connection
                        menuItem.Check()
                        startedTime := connState.GetStartTime()
                        if !startedTime.IsZero() {
                                duration := time.Since(startedTime)
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

                                if isReverseHostByName(hostName) {
                                        if status {
                                                statusIcon = GreenCircle
                                                tooltip = fmt.Sprintf("%s: SSH connection available", hostName)
                                                menuItem.Enable()
                                        } else {
                                                statusIcon = RedCircle
                                                tooltip = fmt.Sprintf("%s: SSH connection failed", hostName)
                                                menuItem.Disable()
                                        }
                                } else {
                                        if status {
                                                statusIcon = GreenCircle
                                                tooltip = fmt.Sprintf("%s: SSH connection available", hostName)
                                                menuItem.Enable()
                                        } else {
                                                statusIcon = RedCircle
                                                tooltip = fmt.Sprintf("%s: SSH connection failed", hostName)
                                                menuItem.Disable()
                                        }
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
                if connState.IsActive() {
                        updateMenuState()
                }
        }
}

// ── Utility functions ────────────────────────────────────────────────────

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

// isReverseHostByName checks if a host (by name) is a reverse host.
func isReverseHostByName(name string) bool {
        if hc := findHostByName(allMenuHosts, name); hc != nil {
                return isReverseHost(*hc)
        }
        return false
}

// getProxyJumpName returns the ProxyJump target name for a host, or empty string.
func getProxyJumpName(name string) string {
        if hc := findHostByName(allMenuHosts, name); hc != nil {
                return hc.ProxyJump
        }
        return ""
}
