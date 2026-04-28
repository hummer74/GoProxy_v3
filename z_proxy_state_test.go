// proxy_state_test.go
// Tests for ProxyState JSON serialization/deserialization (SaveState/LoadState).
// Also tests utility functions that don't require Windows.
package main

import (
        "encoding/json"
        "path/filepath"
        "testing"
        "time"
)

// ---------------------------------------------------------------------------
// ProxyState JSON round-trip
// ---------------------------------------------------------------------------

func TestProxyState_JSONRoundTrip(t *testing.T) {
        original := ProxyState{
                IsChain:          true,
                Host:             "hopper -> worker",
                ChainHosts:       []string{"hopper", "worker"},
                ProxyPort:        1080,
                KeyPath:          "/home/user/.ssh/id_key",
                SSHCommand:       []string{"ssh", "-o", "ProxyJump=hopper", "worker"},
                RemoteHost:       "user@1.2.3.4",
                OriginalHost:     "worker",
                IsFailoverActive: true,
                FailoverStart:    "2026-04-27T10:00:00Z",
        }

        data, err := json.MarshalIndent(original, "", "  ")
        if err != nil {
                t.Fatalf("Marshal: %v", err)
        }

        var restored ProxyState
        if err := json.Unmarshal(data, &restored); err != nil {
                t.Fatalf("Unmarshal: %v", err)
        }

        // Compare fields
        if restored.IsChain != original.IsChain {
                t.Errorf("IsChain: got %v, want %v", restored.IsChain, original.IsChain)
        }
        if restored.Host != original.Host {
                t.Errorf("Host: got %q, want %q", restored.Host, original.Host)
        }
        if len(restored.ChainHosts) != len(original.ChainHosts) {
                t.Fatalf("ChainHosts: got %d entries, want %d", len(restored.ChainHosts), len(original.ChainHosts))
        }
        for i, h := range original.ChainHosts {
                if restored.ChainHosts[i] != h {
                        t.Errorf("ChainHosts[%d]: got %q, want %q", i, restored.ChainHosts[i], h)
                }
        }
        if restored.ProxyPort != original.ProxyPort {
                t.Errorf("ProxyPort: got %d, want %d", restored.ProxyPort, original.ProxyPort)
        }
        if restored.KeyPath != original.KeyPath {
                t.Errorf("KeyPath: got %q, want %q", restored.KeyPath, original.KeyPath)
        }
        if len(restored.SSHCommand) != len(original.SSHCommand) {
                t.Fatalf("SSHCommand: got %d entries, want %d", len(restored.SSHCommand), len(original.SSHCommand))
        }
        if restored.RemoteHost != original.RemoteHost {
                t.Errorf("RemoteHost: got %q, want %q", restored.RemoteHost, original.RemoteHost)
        }
        if restored.OriginalHost != original.OriginalHost {
                t.Errorf("OriginalHost: got %q, want %q", restored.OriginalHost, original.OriginalHost)
        }
        if restored.IsFailoverActive != original.IsFailoverActive {
                t.Errorf("IsFailoverActive: got %v, want %v", restored.IsFailoverActive, original.IsFailoverActive)
        }
        if restored.FailoverStart != original.FailoverStart {
                t.Errorf("FailoverStart: got %q, want %q", restored.FailoverStart, original.FailoverStart)
        }
}

func TestProxyState_SingleHost(t *testing.T) {
        state := ProxyState{
                IsChain:      false,
                Host:         "myserver",
                ProxyPort:    1080,
                RemoteHost:   "user@5.6.7.8",
                OriginalHost: "myserver",
        }

        data, err := json.Marshal(state)
        if err != nil {
                t.Fatalf("Marshal: %v", err)
        }

        var restored ProxyState
        if err := json.Unmarshal(data, &restored); err != nil {
                t.Fatalf("Unmarshal: %v", err)
        }

        if restored.IsChain {
                t.Error("single host: IsChain should be false")
        }
        if restored.Host != "myserver" {
                t.Errorf("Host: got %q, want %q", restored.Host, "myserver")
        }
        if len(restored.ChainHosts) != 0 {
                t.Errorf("ChainHosts: got %d entries, want 0", len(restored.ChainHosts))
        }
}

func TestProxyState_FailoverStart_OmitEmpty(t *testing.T) {
        // When FailoverStart is empty, it should be omitted in JSON
        state := ProxyState{
                Host: "test",
        }

        data, err := json.Marshal(state)
        if err != nil {
                t.Fatalf("Marshal: %v", err)
        }

        var raw map[string]interface{}
        if err := json.Unmarshal(data, &raw); err != nil {
                t.Fatalf("Unmarshal to map: %v", err)
        }

        if _, exists := raw["failover_start"]; exists {
                t.Error("failover_start should be omitted when empty (omitempty)")
        }
}

// ---------------------------------------------------------------------------
// SaveState / LoadState (file-based)
// ---------------------------------------------------------------------------

func TestSaveAndLoadState(t *testing.T) {
        tmpDir := t.TempDir()
        stateFile := filepath.Join(tmpDir, "x_proxy_state.json")

        // Setup Config paths
        Config = &AppConfig{}
        Config.TempFiles.StateFile = stateFile

        state := ProxyState{
                IsChain:      false,
                Host:         "testhost",
                ProxyPort:    9999,
                RemoteHost:   "root@10.0.0.1",
                OriginalHost: "testhost",
                SSHCommand:   []string{"ssh", "-D", "9999", "root@10.0.0.1"},
        }

        if err := SaveState(state); err != nil {
                t.Fatalf("SaveState: %v", err)
        }

        loaded, err := LoadState()
        if err != nil {
                t.Fatalf("LoadState: %v", err)
        }

        if loaded.Host != state.Host {
                t.Errorf("Host: got %q, want %q", loaded.Host, state.Host)
        }
        if loaded.ProxyPort != state.ProxyPort {
                t.Errorf("ProxyPort: got %d, want %d", loaded.ProxyPort, state.ProxyPort)
        }
}

func TestLoadState_NotFound(t *testing.T) {
        Config = &AppConfig{}
        Config.TempFiles.StateFile = "/nonexistent/path/state.json"

        _, err := LoadState()
        if err == nil {
                t.Error("LoadState on missing file should return error")
        }
}

// ---------------------------------------------------------------------------
// SaveLastHost / LoadLastHost
// ---------------------------------------------------------------------------

func TestSaveAndLoadLastHost(t *testing.T) {
        tmpDir := t.TempDir()
        lastHostFile := filepath.Join(tmpDir, "x_lasthost.cfg")

        Config = &AppConfig{}
        Config.Paths.LastHostFile = lastHostFile

        if err := SaveLastHost("myserver"); err != nil {
                t.Fatalf("SaveLastHost: %v", err)
        }

        loaded := LoadLastHost()
        if loaded != "myserver" {
                t.Errorf("LoadLastHost: got %q, want %q", loaded, "myserver")
        }
}

func TestLoadLastHost_NotFound(t *testing.T) {
        Config = &AppConfig{}
        Config.Paths.LastHostFile = "/nonexistent/path/lasthost.cfg"

        loaded := LoadLastHost()
        if loaded != "" {
                t.Errorf("LoadLastHost on missing file: got %q, want empty", loaded)
        }
}

// ---------------------------------------------------------------------------
// Utility: findHostByName
// ---------------------------------------------------------------------------

func TestFindHostByName(t *testing.T) {
        hosts := []HostConfig{
                {Name: "alpha", HostName: "1.1.1.1"},
                {Name: "beta", HostName: "2.2.2.2"},
                {Name: "gamma", HostName: "3.3.3.3"},
        }

        h := findHostByName(hosts, "beta")
        if h == nil {
                t.Fatal("findHostByName(beta) = nil")
        }
        if h.HostName != "2.2.2.2" {
                t.Errorf("HostName: got %q, want %q", h.HostName, "2.2.2.2")
        }

        if h := findHostByName(hosts, "nonexistent"); h != nil {
                t.Error("findHostByName(nonexistent) should be nil")
        }
}

// ---------------------------------------------------------------------------
// Utility: isReverseHost / isReverseJumperHost
// ---------------------------------------------------------------------------

func TestIsReverseHost(t *testing.T) {
        tests := []struct {
                host HostConfig
                want bool
        }{
                {HostConfig{Name: "direct", ProxyJump: ""}, false},
                {HostConfig{Name: "reverse", ProxyJump: "jumper-host"}, true},
        }
        for _, tt := range tests {
                got := isReverseHost(tt.host)
                if got != tt.want {
                        t.Errorf("isReverseHost(%+v) = %v, want %v", tt.host, got, tt.want)
                }
        }
}

func TestIsReverseJumperHost(t *testing.T) {
        tests := []struct {
                host HostConfig
                want bool
        }{
                {HostConfig{Name: "normal", Group: "GROUP1"}, false},
                {HostConfig{Name: "jumper", Group: "REVERSE JUMPER"}, true},
                {HostConfig{Name: "partial", Group: "REVERSE JUMPER GROUP2"}, true},
        }
        for _, tt := range tests {
                got := isReverseJumperHost(tt.host)
                if got != tt.want {
                        t.Errorf("isReverseJumperHost(%+v) = %v, want %v", tt.host, got, tt.want)
                }
        }
}

// ---------------------------------------------------------------------------
// Utility: formatDuration
// ---------------------------------------------------------------------------

func TestFormatDuration(t *testing.T) {
        tests := []struct {
                d    time.Duration
                want string
        }{
                {0, "00:00"},
                {1 * time.Minute, "00:01"},
                {59 * time.Minute, "00:59"},
                {1 * time.Hour, "01:00"},
                {1*time.Hour + 30*time.Minute, "01:30"},
                {23*time.Hour + 59*time.Minute, "23:59"},
                {24*time.Hour, "00:00"}, // wraps around (modulo 24h)
        }
        for _, tt := range tests {
                got := formatDuration(tt.d)
                if got != tt.want {
                        t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
                }
        }
}

// ---------------------------------------------------------------------------
// Utility: padRight
// ---------------------------------------------------------------------------

func TestPadRight(t *testing.T) {
        if got := padRight("ab", 5); got != "ab   " {
                t.Errorf("padRight(ab, 5) = %q, want %q", got, "ab   ")
        }
        if got := padRight("abcde", 3); got != "abcde" {
                t.Errorf("padRight(abcde, 3) = %q, want %q (no truncate)", got, "abcde")
        }
        if got := padRight("a", 1); got != "a" {
                t.Errorf("padRight(a, 1) = %q, want %q", got, "a")
        }
}

// ---------------------------------------------------------------------------
// hostsByGroup
// ---------------------------------------------------------------------------

func TestHostsByGroup(t *testing.T) {
        hosts := []HostConfig{
                {Name: "h1", Group: "A"},
                {Name: "h2", Group: "B"},
                {Name: "h3", Group: "A"},
                {Name: "h4", Group: "B"},
                {Name: "h5", Group: "C"},
        }

        groups := hostsByGroup(hosts)

        if len(groups) != 3 {
                t.Fatalf("got %d groups, want 3", len(groups))
        }

        // First group should be "A" (first seen)
        if groups[0].Name != "A" || len(groups[0].Hosts) != 2 {
                t.Errorf("group A: name=%q, hosts=%d", groups[0].Name, len(groups[0].Hosts))
        }
        if groups[1].Name != "B" || len(groups[1].Hosts) != 2 {
                t.Errorf("group B: name=%q, hosts=%d", groups[1].Name, len(groups[1].Hosts))
        }
        if groups[2].Name != "C" || len(groups[2].Hosts) != 1 {
                t.Errorf("group C: name=%q, hosts=%d", groups[2].Name, len(groups[2].Hosts))
        }
}
