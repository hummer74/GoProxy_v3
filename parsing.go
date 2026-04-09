package main

import (
        "bufio"
        "os"
        "strings"
        "sync"
        "time"
)

// sshConfigCache provides thread-safe caching for parsed SSH config.
// The cache is invalidated when the file's modification time changes.
type sshConfigCache struct {
        mu        sync.RWMutex
        path      string
        modTime   time.Time
        hosts     []HostConfig
        parsed    bool
}

var configCache = &sshConfigCache{}

// getCachedHosts returns cached hosts if the config file hasn't changed.
// Returns nil if cache is invalid or empty.
func (c *sshConfigCache) getCachedHosts(path string) []HostConfig {
        c.mu.RLock()
        defer c.mu.RUnlock()

        if !c.parsed || c.path != path {
                return nil
        }

        stat, err := os.Stat(path)
        if err != nil {
                return nil // file gone or unreadable
        }

        if stat.ModTime() != c.modTime {
                return nil // file changed
        }

        return c.hosts
}

// setCachedHosts stores parsed hosts in the cache.
func (c *sshConfigCache) setCachedHosts(path string, hosts []HostConfig) {
        c.mu.Lock()
        defer c.mu.Unlock()

        stat, err := os.Stat(path)
        if err != nil {
                return
        }

        c.path = path
        c.modTime = stat.ModTime()
        c.hosts = hosts
        c.parsed = true
}

// invalidate clears the cache (e.g. when config path changes).
func (c *sshConfigCache) invalidate() {
        c.mu.Lock()
        defer c.mu.Unlock()
        c.parsed = false
        c.hosts = nil
}

// parseSSHConfig reads and parses the SSH config file.
// Results are cached per file path; the cache is invalidated when the file's
// modification time changes, so manual edits to ~/.ssh/config are picked up
// within one polling cycle.
func parseSSHConfig(path string) []HostConfig {
        // Check cache first
        if cached := configCache.getCachedHosts(path); cached != nil {
                return cached
        }

        // Cache miss — read from disk
        file, err := os.Open(path)
        if err != nil {
                printWarn("Failed to open SSH config: " + err.Error())
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

        printInfo("Parsed hosts from SSH config")

        // Store in cache
        configCache.setCachedHosts(path, hosts)

        return hosts
}
