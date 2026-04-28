// x_utils_test.go
// Tests for utility functions: resolveSSHKeyPath, savePid, GetCircledNumber,
// LoadPriorityHost, ClearPriorityHost.
package main

import (
        "encoding/json"
        "os"
        "path/filepath"
        "testing"
)

// ---------------------------------------------------------------------------
// resolveSSHKeyPath
// ---------------------------------------------------------------------------

func TestResolveSSHKeyPath_AbsolutePath(t *testing.T) {
        // Absolute paths should be returned as-is, regardless of workdir.
        // Use a guaranteed-absolute directory (TempDir is always absolute).
        tmpDir := t.TempDir()
        absKey := filepath.Join(tmpDir, "home", "user", ".ssh", "id_key")

        result := resolveSSHKeyPath(tmpDir, absKey)
        if result != absKey {
                t.Errorf("absolute path = %q, want %q", result, absKey)
        }
}

func TestResolveSSHKeyPath_EmptyKey(t *testing.T) {
        result := resolveSSHKeyPath("/workdir", "")
        if result != "" {
                t.Errorf("empty key = %q, want empty", result)
        }
}

func TestResolveSSHKeyPath_RelativeInDotSSH(t *testing.T) {
        tmpDir := t.TempDir()
        sshDir := filepath.Join(tmpDir, ".ssh")
        os.MkdirAll(sshDir, 0755)

        keyFile := filepath.Join(sshDir, "id_custom")
        os.WriteFile(keyFile, []byte("key-data"), 0600)

        result := resolveSSHKeyPath(tmpDir, "id_custom")
        if result != keyFile {
                t.Errorf("relative key in .ssh = %q, want %q", result, keyFile)
        }
}

func TestResolveSSHKeyPath_RelativeNoDotSSH(t *testing.T) {
        tmpDir := t.TempDir()

        keyFile := filepath.Join(tmpDir, "id_direct")
        os.WriteFile(keyFile, []byte("key-data"), 0600)

        result := resolveSSHKeyPath(tmpDir, "id_direct")
        if result != keyFile {
                t.Errorf("relative key (no .ssh) = %q, want %q", result, keyFile)
        }
}

// ---------------------------------------------------------------------------
// savePid / PidData
// ---------------------------------------------------------------------------

func TestSavePid_WritesValidJSON(t *testing.T) {
        tmpDir := t.TempDir()
        pidFile := filepath.Join(tmpDir, "test.pid")

        savePid(pidFile, 12345, "SSH Tunnel")

        data, err := os.ReadFile(pidFile)
        if err != nil {
                t.Fatalf("read pid file: %v", err)
        }

        var pd PidData
        if err := json.Unmarshal(data, &pd); err != nil {
                t.Fatalf("unmarshal pid data: %v", err)
        }

        if pd.Pid != 12345 {
                t.Errorf("Pid = %d, want 12345", pd.Pid)
        }
        if pd.Info != "SSH Tunnel" {
                t.Errorf("Info = %q, want %q", pd.Info, "SSH Tunnel")
        }
}

// ---------------------------------------------------------------------------
// GetCircledNumber
// ---------------------------------------------------------------------------

func TestGetCircledNumber(t *testing.T) {
        tests := []struct {
                pos  int
                want string
        }{
                {1, "①"},
                {2, "②"},
                {5, "⑤"},
                {10, "⑩"},
                {0, "?"},
                {11, "?"},
                {-1, "?"},
        }
        for _, tt := range tests {
                got := GetCircledNumber(tt.pos)
                if got != tt.want {
                        t.Errorf("GetCircledNumber(%d) = %q, want %q", tt.pos, got, tt.want)
                }
        }
}

// ---------------------------------------------------------------------------
// Priority host functions
// ---------------------------------------------------------------------------

func TestPriorityHost_ClearAndLoad(t *testing.T) {
        // Clear first
        ClearPriorityHost()
        if priorityHost != "" {
                t.Errorf("priorityHost after clear = %q, want empty", priorityHost)
        }
        if hasPriorityHost {
                t.Error("hasPriorityHost should be false after clear")
        }

        // Set directly (simulating LoadPriorityHost behavior)
        priorityHost = "my-priority-host"
        hasPriorityHost = true

        if GetPriorityHost() != "my-priority-host" {
                t.Errorf("GetPriorityHost() = %q, want %q", GetPriorityHost(), "my-priority-host")
        }
        if !HasPriorityHost() {
                t.Error("HasPriorityHost() should be true")
        }

        // Clean up
        ClearPriorityHost()
}

// ---------------------------------------------------------------------------
// LoadPriorityHost from file
// ---------------------------------------------------------------------------

func TestLoadPriorityHost_FromFile(t *testing.T) {
        tmpDir := t.TempDir()
        priorityPath := filepath.Join(tmpDir, ".ssh", "x_lasthost.cfg")
        os.MkdirAll(filepath.Join(tmpDir, ".ssh"), 0755)
        os.WriteFile(priorityPath, []byte("  my-priority  \n"), 0644)

        // Setup config
        Config = &AppConfig{}
        Config.Paths.WorkDir = tmpDir

        // Clear first
        ClearPriorityHost()

        result := LoadPriorityHost()
        if result != "my-priority" {
                t.Errorf("LoadPriorityHost() = %q, want %q (trimmed)", result, "my-priority")
        }
        if !HasPriorityHost() {
                t.Error("HasPriorityHost() should be true after load")
        }
}

func TestLoadPriorityHost_NoFile(t *testing.T) {
        Config = &AppConfig{}
        Config.Paths.WorkDir = "/nonexistent"

        ClearPriorityHost()
        result := LoadPriorityHost()
        if result != "" {
                t.Errorf("LoadPriorityHost() = %q, want empty (no file)", result)
        }
        if HasPriorityHost() {
                t.Error("HasPriorityHost() should be false when no file")
        }
}

func TestLoadPriorityHost_EmptyFile(t *testing.T) {
        tmpDir := t.TempDir()
        priorityPath := filepath.Join(tmpDir, ".ssh", "x_lasthost.cfg")
        os.MkdirAll(filepath.Join(tmpDir, ".ssh"), 0755)
        os.WriteFile(priorityPath, []byte("   \n"), 0644)

        Config = &AppConfig{}
        Config.Paths.WorkDir = tmpDir

        ClearPriorityHost()
        result := LoadPriorityHost()
        if result != "" {
                t.Errorf("LoadPriorityHost() = %q, want empty (whitespace only)", result)
        }
}

// ---------------------------------------------------------------------------
// PidData JSON round-trip
// ---------------------------------------------------------------------------

func TestPidData_JSONRoundTrip(t *testing.T) {
        original := PidData{Pid: 9999, Info: "Test Process"}

        data, err := json.Marshal(original)
        if err != nil {
                t.Fatalf("Marshal: %v", err)
        }

        var restored PidData
        if err := json.Unmarshal(data, &restored); err != nil {
                t.Fatalf("Unmarshal: %v", err)
        }

        if restored.Pid != original.Pid {
                t.Errorf("Pid = %d, want %d", restored.Pid, original.Pid)
        }
        if restored.Info != original.Info {
                t.Errorf("Info = %q, want %q", restored.Info, original.Info)
        }
}

// ---------------------------------------------------------------------------
// HostStatusWithTime
// ---------------------------------------------------------------------------

func TestHostStatusWithTime_Fields(t *testing.T) {
        host := HostConfig{Name: "test", HostName: "1.2.3.4"}
        status := HostStatusWithTime{
                Host:         host,
                Available:    true,
                ResponseTime: 1500000000, // 1.5s in nanoseconds
        }

        if !status.Available {
                t.Error("Available should be true")
        }
        if status.Host.Name != "test" {
                t.Errorf("Host.Name = %q, want %q", status.Host.Name, "test")
        }
        if status.ResponseTime <= 0 {
                t.Error("ResponseTime should be positive")
        }
}
