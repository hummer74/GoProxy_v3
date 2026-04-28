// ssh_checker_test.go
// Regression test for Fix2: checkSSHConnectionAdvanced must delegate to
// checkSSHConnectionWithTime and return consistent results.
//
// Strategy: replace the function hook (checkSSHConnectionWithTimeFn) with a
// controlled stub. Then verify that checkSSHConnectionAdvanced returns the
// same boolean that the stub returns.
package main

import (
        "sync"
        "testing"
        "time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Regression: checkSSHConnectionAdvanced delegates to checkSSHConnectionWithTime
// ---------------------------------------------------------------------------

func TestCheckSSHConnectionAdvanced_DelegatesToWithTime(t *testing.T) {
        // Save original hook
        orig := checkSSHConnectionWithTimeFn
        defer func() { checkSSHConnectionWithTimeFn = orig }()

        host := HostConfig{Name: "test-host", HostName: "1.2.3.4", Port: "22"}

        // Case 1: stub returns true
        var callCount int
        checkSSHConnectionWithTimeFn = func(h HostConfig, _ string) (bool, time.Duration) {
                callCount++
                if h.Name != host.Name {
                        t.Errorf("stub received host %q, want %q", h.Name, host.Name)
                }
                return true, 500 * time.Millisecond
        }

        result := checkSSHConnectionAdvanced(host, "/workdir")
        if !result {
                t.Error("checkSSHConnectionAdvanced() = false, want true (stub returns true)")
        }
        if callCount != 1 {
                t.Errorf("stub called %d times, want 1", callCount)
        }

        // Case 2: stub returns false
        callCount = 0
        checkSSHConnectionWithTimeFn = func(_ HostConfig, _ string) (bool, time.Duration) {
                callCount++
                return false, 0
        }

        result = checkSSHConnectionAdvanced(host, "/workdir")
        if result {
                t.Error("checkSSHConnectionAdvanced() = true, want false (stub returns false)")
        }
        if callCount != 1 {
                t.Errorf("stub called %d times, want 1", callCount)
        }
}

// ---------------------------------------------------------------------------
// Regression: consistent result for "unknown" errors
//
// Before Fix2, checkSSHConnectionAdvanced returned true for unknown SSH errors
// while checkSSHConnectionWithTime returned false. After Fix2, both MUST return
// the same value because Advanced delegates to WithTime.
// ---------------------------------------------------------------------------

func TestCheckSSH_ConsistentResults_UnknownError(t *testing.T) {
        orig := checkSSHConnectionWithTimeFn
        defer func() { checkSSHConnectionWithTimeFn = orig }()

        host := HostConfig{Name: "ambiguous-host", HostName: "5.6.7.8", Port: "22"}

        // Simulate "unknown error" → WithTime returns false (safe default)
        checkSSHConnectionWithTimeFn = func(_ HostConfig, _ string) (bool, time.Duration) {
                return false, 0 // unknown error → false
        }

        // Advanced now delegates to the hook (checkSSHConnectionWithTimeFn)
        // Verify it returns what the hook says
        advancedResult := checkSSHConnectionAdvanced(host, "/workdir")

        if advancedResult {
                t.Error("advancedResult = true for unknown error, want false (safe default)")
        }
}

// ---------------------------------------------------------------------------
// Concurrent safety: multiple goroutines calling Advanced + WithTime
// (triggers race detector if there's a data race)
// ---------------------------------------------------------------------------

func TestCheckSSH_ConcurrentCalls(t *testing.T) {
        orig := checkSSHConnectionWithTimeFn
        defer func() { checkSSHConnectionWithTimeFn = orig }()

        // Simple deterministic stub
        checkSSHConnectionWithTimeFn = func(_ HostConfig, _ string) (bool, time.Duration) {
                time.Sleep(time.Millisecond) // simulate work
                return true, 10 * time.Millisecond
        }

        host := HostConfig{Name: "concurrent-host", HostName: "9.9.9.9", Port: "22"}

        var wg sync.WaitGroup
        for i := 0; i < 20; i++ {
                wg.Add(2)

                go func() {
                        defer wg.Done()
                        checkSSHConnectionAdvanced(host, "/workdir")
                }()

                go func() {
                        defer wg.Done()
                        checkSSHConnectionWithTimeFn(host, "/workdir")
                }()
        }
        wg.Wait()
}
