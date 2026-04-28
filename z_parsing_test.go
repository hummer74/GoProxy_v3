// x_parsing_test.go
// Tests for SSH config parser: parseSSHConfig with mock config files.
package main

import (
        "os"
        "path/filepath"
        "strings"
        "testing"
        "time"
)

// ---------------------------------------------------------------------------
// parseSSHConfig — mock SSH config file parsing
// ---------------------------------------------------------------------------

func TestParseSSHConfig_BasicHosts(t *testing.T) {
        content := `Host myserver
    HostName 192.168.1.100
    User root
    Port 22
    IdentityFile ~/.ssh/id_key

Host another
    HostName 10.0.0.5
    User admin
    Port 2222
`
        hosts := parseTestConfig(t, content)

        if len(hosts) != 2 {
                t.Fatalf("got %d hosts, want 2", len(hosts))
        }

        if hosts[0].Name != "myserver" {
                t.Errorf("hosts[0].Name = %q, want %q", hosts[0].Name, "myserver")
        }
        if hosts[0].HostName != "192.168.1.100" {
                t.Errorf("hosts[0].HostName = %q, want %q", hosts[0].HostName, "192.168.1.100")
        }
        if hosts[0].User != "root" {
                t.Errorf("hosts[0].User = %q, want %q", hosts[0].User, "root")
        }
        if hosts[0].Port != "22" {
                t.Errorf("hosts[0].Port = %q, want %q", hosts[0].Port, "22")
        }

        if hosts[1].Name != "another" {
                t.Errorf("hosts[1].Name = %q, want %q", hosts[1].Name, "another")
        }
        if hosts[1].Port != "2222" {
                t.Errorf("hosts[1].Port = %q, want %q", hosts[1].Port, "2222")
        }
}

func TestParseSSHConfig_WithProxyJump(t *testing.T) {
        content := `Host direct-host
    HostName 10.0.0.1
    User root

Host reverse-host
    HostName 10.0.0.2
    User admin
    ProxyJump jumper-host
`
        hosts := parseTestConfig(t, content)

        if len(hosts) != 2 {
                t.Fatalf("got %d hosts, want 2", len(hosts))
        }

        if hosts[0].ProxyJump != "" {
                t.Errorf("direct host ProxyJump = %q, want empty", hosts[0].ProxyJump)
        }
        if hosts[1].ProxyJump != "jumper-host" {
                t.Errorf("reverse host ProxyJump = %q, want %q", hosts[1].ProxyJump, "jumper-host")
        }
}

func TestParseSSHConfig_WithGroups(t *testing.T) {
        content := `####GROUP1####
# GROUP1

Host host-a
    HostName 10.0.0.1
    User root

####GROUP2####
# GROUP2

Host host-b
    HostName 10.0.0.2
    User admin
`
        hosts := parseTestConfig(t, content)

        if len(hosts) != 2 {
                t.Fatalf("got %d hosts, want 2", len(hosts))
        }

        if hosts[0].Group != "GROUP1" {
                t.Errorf("hosts[0].Group = %q, want %q", hosts[0].Group, "GROUP1")
        }
        if hosts[1].Group != "GROUP2" {
                t.Errorf("hosts[1].Group = %q, want %q", hosts[1].Group, "GROUP2")
        }
}

func TestParseSSHConfig_IdentityFile(t *testing.T) {
        content := `Host mykeyhost
    HostName 10.0.0.3
    IdentityFile ~/.ssh/custom_key
`
        hosts := parseTestConfig(t, content)

        if len(hosts) != 1 {
                t.Fatalf("got %d hosts, want 1", len(hosts))
        }
        if hosts[0].IdentityFile != "~/.ssh/custom_key" {
                t.Errorf("IdentityFile = %q, want %q", hosts[0].IdentityFile, "~/.ssh/custom_key")
        }
}

func TestParseSSHConfig_SkipsWildcard(t *testing.T) {
        content := `Host *
    ServerAliveInterval 60
    ServerAliveCountMax 3

Host real-host
    HostName 10.0.0.4
`
        hosts := parseTestConfig(t, content)

        for _, h := range hosts {
                if h.Name == "*" {
                        t.Error("wildcard host '*' should not be in parsed hosts")
                }
        }

        if len(hosts) != 1 {
                t.Fatalf("got %d hosts, want 1", len(hosts))
        }
        if hosts[0].Name != "real-host" {
                t.Errorf("Name = %q, want %q", hosts[0].Name, "real-host")
        }
}

func TestParseSSHConfig_EmptyFile(t *testing.T) {
        content := `# Just a comment

`
        hosts := parseTestConfig(t, content)
        if len(hosts) != 0 {
                t.Errorf("got %d hosts from empty config, want 0", len(hosts))
        }
}

func TestParseSSHConfig_ParamsStored(t *testing.T) {
        content := `Host param-host
    HostName 10.0.0.5
    User root
    IdentitiesOnly yes
    ForwardAgent yes
`
        hosts := parseTestConfig(t, content)

        if len(hosts) != 1 {
                t.Fatalf("got %d hosts, want 1", len(hosts))
        }

        if hosts[0].Params["identitiesonly"] != "yes" {
                t.Errorf("Params[identitiesonly] = %q, want %q", hosts[0].Params["identitiesonly"], "yes")
        }
        if hosts[0].Params["forwardagent"] != "yes" {
                t.Errorf("Params[forwardagent] = %q, want %q", hosts[0].Params["forwardagent"], "yes")
        }
        if hosts[0].Params["hostname"] != "10.0.0.5" {
                t.Errorf("Params[hostname] = %q, want %q", hosts[0].Params["hostname"], "10.0.0.5")
        }
}

func TestParseSSHConfig_MultipleIdentityFiles(t *testing.T) {
        content := `Host multi-key
    HostName 10.0.0.6
    IdentityFile ~/.ssh/id_ed25519
    IdentityFile ~/.ssh/id_rsa
`
        hosts := parseTestConfig(t, content)

        if len(hosts) != 1 {
                t.Fatalf("got %d hosts, want 1", len(hosts))
        }
        // Parser keeps only the first IdentityFile
        if hosts[0].IdentityFile != "~/.ssh/id_ed25519" {
                t.Errorf("IdentityFile = %q, want %q (first one)", hosts[0].IdentityFile, "~/.ssh/id_ed25519")
        }
}

// ---------------------------------------------------------------------------
// SSH config cache
// ---------------------------------------------------------------------------

func TestParseSSHConfig_CacheInvalidation(t *testing.T) {
        content := `Host v1-host
    HostName 10.0.0.1
`
        tmpDir := t.TempDir()
        cfgPath := filepath.Join(tmpDir, "config")

        os.WriteFile(cfgPath, []byte(content), 0644)
        configCache.invalidate()

        hosts1 := parseSSHConfig(cfgPath)
        if len(hosts1) != 1 {
                t.Fatalf("parse 1: got %d hosts", len(hosts1))
        }

        // Modify file (touch to change mtime, write new content)
        newContent := `Host v1-host
    HostName 10.0.0.1

Host v2-host
    HostName 10.0.0.2
`
        // Ensure mtime changes by adding a small delay
        time.Sleep(10 * time.Millisecond)
        os.WriteFile(cfgPath, []byte(newContent), 0644)

        hosts2 := parseSSHConfig(cfgPath)
        if len(hosts2) != 2 {
                t.Fatalf("parse 2 (after modification): got %d hosts, want 2", len(hosts2))
        }
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func parseTestConfig(t *testing.T, content string) []HostConfig {
        t.Helper()
        tmpDir := t.TempDir()
        cfgPath := filepath.Join(tmpDir, "ssh_config")

        content = strings.ReplaceAll(content, "\r\n", "\n")
        if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
                t.Fatalf("write test config: %v", err)
        }

        configCache.invalidate()
        return parseSSHConfig(cfgPath)
}
