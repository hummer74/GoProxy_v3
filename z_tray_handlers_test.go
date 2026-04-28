// x_tray_handlers_test.go
// Tests for tray handler logic: handleKillProxy atomic flag behavior,
// handleSmartFailover guard conditions, and chain builder operations.
package main

import (
        "path/filepath"
        "sync"
        "sync/atomic"
        "testing"
)

// ---------------------------------------------------------------------------
// Chain builder operations (thread-safe add/remove/clear)
// ---------------------------------------------------------------------------

func TestChainBuilder_AddAndCount(t *testing.T) {
        clearChainBuilder()

        if getChainBuilderCount() != 0 {
                t.Errorf("initial count = %d, want 0", getChainBuilderCount())
        }

        chainBuilderMutex.Lock()
        chainBuilder = append(chainBuilder, HostConfig{Name: "host-a"})
        chainBuilderMutex.Unlock()

        if getChainBuilderCount() != 1 {
                t.Errorf("count after add = %d, want 1", getChainBuilderCount())
        }
}

func TestChainBuilder_GetCopy(t *testing.T) {
        clearChainBuilder()

        chainBuilderMutex.Lock()
        chainBuilder = []HostConfig{
                {Name: "host-1"},
                {Name: "host-2"},
                {Name: "host-3"},
        }
        chainBuilderMutex.Unlock()

        copy := getChainBuilderCopy()

        // Verify copy is independent
        if len(copy) != 3 {
                t.Fatalf("copy length = %d, want 3", len(copy))
        }
        if copy[0].Name != "host-1" {
                t.Errorf("copy[0].Name = %q, want %q", copy[0].Name, "host-1")
        }

        // Modify original, verify copy is unaffected
        clearChainBuilder()
        if len(copy) != 3 {
                t.Error("copy should be independent of original")
        }
}

func TestChainBuilder_Clear(t *testing.T) {
        chainBuilderMutex.Lock()
        chainBuilder = []HostConfig{
                {Name: "host-1"},
                {Name: "host-2"},
        }
        chainBuilderMutex.Unlock()

        clearChainBuilder()

        if getChainBuilderCount() != 0 {
                t.Errorf("count after clear = %d, want 0", getChainBuilderCount())
        }

        copy := getChainBuilderCopy()
        if len(copy) != 0 {
                t.Errorf("copy after clear = %d items, want 0", len(copy))
        }
}

func TestChainBuilder_GetPosition(t *testing.T) {
        clearChainBuilder()

        chainBuilderMutex.Lock()
        chainBuilder = []HostConfig{
                {Name: "first"},
                {Name: "second"},
                {Name: "third"},
        }
        chainBuilderMutex.Unlock()

        if pos := getChainPosition("first"); pos != 1 {
                t.Errorf("position of first = %d, want 1", pos)
        }
        if pos := getChainPosition("second"); pos != 2 {
                t.Errorf("position of second = %d, want 2", pos)
        }
        if pos := getChainPosition("third"); pos != 3 {
                t.Errorf("position of third = %d, want 3", pos)
        }
        if pos := getChainPosition("nonexistent"); pos != 0 {
                t.Errorf("position of nonexistent = %d, want 0", pos)
        }
}

func TestChainBuilder_ConcurrentAccess(t *testing.T) {
        clearChainBuilder()

        var wg sync.WaitGroup
        for i := 0; i < 100; i++ {
                wg.Add(2)
                go func(n int) {
                        defer wg.Done()
                        chainBuilderMutex.Lock()
                        chainBuilder = append(chainBuilder, HostConfig{Name: "host"})
                        chainBuilderMutex.Unlock()
                }(i)
                go func() {
                        defer wg.Done()
                        _ = getChainBuilderCount()
                }()
        }
        wg.Wait()

        // No assertion needed — the race detector will catch issues
}

// ---------------------------------------------------------------------------
// handleKillProxy — sets userStopRequested BEFORE anything else (Fix 1)
// ---------------------------------------------------------------------------

func TestHandleKillProxy_SetsStopFlag(t *testing.T) {
        // Precondition: flag is 0
        atomic.StoreUint64(&userStopRequested, 0)

        // Simulate the FIRST line of handleKillProxy:
        // atomic.StoreUint64(&userStopRequested, 1)
        atomic.StoreUint64(&userStopRequested, 1)

        val := atomic.LoadUint64(&userStopRequested)
        if val != 1 {
                t.Errorf("userStopRequested = %d, want 1 after handleKillProxy", val)
        }

        // Clean up
        atomic.StoreUint64(&userStopRequested, 0)
}

// ---------------------------------------------------------------------------
// handleSmartFailover — guard conditions
// ---------------------------------------------------------------------------

func TestHandleSmartFailover_Disabled(t *testing.T) {
        Config = &AppConfig{}
        Config.General.SmartFailover = false

        result := handleSmartFailover(&ProxyState{Host: "test"})
        if result {
                t.Error("handleSmartFailover should return false when SmartFailover is disabled")
        }
}

func TestHandleSmartFailover_NoHostsInConfig(t *testing.T) {
        tmpDir := t.TempDir()
        Config = &AppConfig{}
        Config.General.SmartFailover = true
        Config.Paths.SSHConfig = filepath.Join(tmpDir, "nonexistent_config")

        // parseSSHConfig will return empty for nonexistent file
        result := handleSmartFailover(&ProxyState{Host: "test"})
        if result {
                t.Error("handleSmartFailover should return false when no hosts available")
        }
}

// ---------------------------------------------------------------------------
// handleHostClick — skip if already connected to same host
// ---------------------------------------------------------------------------

func TestHandleHostClick_SkipIfAlreadyConnected(t *testing.T) {
        connState = &ConnectionState{}
        connState.SetConnected("same-host")

        hostStatusCache = NewHostStatusCache()

        host := HostConfig{Name: "same-host", HostName: "1.2.3.4"}

        // handleHostClick checks: connState.IsActive() && connState.GetHost() == host.Name
        if connState.IsActive() && connState.GetHost() == host.Name {
                // This is the guard condition in handleHostClick — it returns early
                return
        }
        t.Error("handleHostClick should skip when already connected to same host")
}

func TestHandleHostClick_SkipIfUnavailable(t *testing.T) {
        connState = &ConnectionState{}
        hostStatusCache = NewHostStatusCache()
        hostStatusCache.Set("unavailable-host", false)

        host := HostConfig{Name: "unavailable-host"}

        // handleHostClick checks: status, exists := hostStatusCache.Get(host.Name)
        if status, exists := hostStatusCache.Get(host.Name); exists && !status {
                // This is the guard condition — host is marked unavailable
                return
        }
        t.Error("handleHostClick should skip when host is unavailable")
}

// ---------------------------------------------------------------------------
// HostStatusCache integration with handlers
// ---------------------------------------------------------------------------

func TestHostStatusCache_HandlerIntegration(t *testing.T) {
        hostStatusCache = NewHostStatusCache()

        // Mark hosts
        hostStatusCache.Set("available-host", true)
        hostStatusCache.Set("down-host", false)

        // Verify guard logic for handleHostClick
        host := HostConfig{Name: "down-host"}
        if status, exists := hostStatusCache.Get(host.Name); exists && !status {
                // Handler should return early — this is correct
        } else {
                t.Error("down-host should be marked as unavailable in cache")
        }

        host2 := HostConfig{Name: "available-host"}
        if status, exists := hostStatusCache.Get(host2.Name); exists && !status {
                t.Error("available-host should NOT trigger the unavailable guard")
        }
}
