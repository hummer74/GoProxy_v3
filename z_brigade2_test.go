// z_brigade2_test.go — table-driven tests for SSH error classification,
// generate_204 health checks, PAC URL ownership, and tunnel probing.
package main

import (
	"fmt"
	"net"
	"testing"
	"time"
)

// TestBrigadeClassifySSHErr checks that ssh stderr maps to auth vs net classes.
func TestBrigadeClassifySSHErr(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   string
	}{
		{"publickey denied", "Permission denied (publickey,gssapi-keyex,gssapi-with-mic).", "auth"},
		{"connection timed out", "ssh: connect to host 10.0.0.1 port 22: Connection timed out", "net"},
		{"no route to host", "ssh: connect to host 10.0.0.2 port 22: No route to host", "net"},
		{"bad passphrase", "Bad passphrase, try again for /home/user/.ssh/id_rsa", "auth"},
		{"empty", "", "net"},
		{"unresolvable hostname", "ssh: Could not resolve hostname ghost.invalid: Name or service not known", "net"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifySSHErr(c.stderr); got != c.want {
				t.Errorf("classifySSHErr(%q) = %q, want %q", c.stderr, got, c.want)
			}
		})
	}
}

// TestBrigadeHealthyGenerate checks generate_204 health validation.
func TestBrigadeHealthyGenerate(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		bodyLen int64
		want    bool
	}{
		{"empty 204", 204, 0, true},
		{"small body 204", 204, 100, true},
		{"login page on 204", 204, 5000, false},
		{"200 with body", 200, 10, false},
		{"redirect", 302, 10, false},
		{"not found", 404, 0, false},
		{"server error", 500, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isHealthyGenerate204(c.status, c.bodyLen); got != c.want {
				t.Errorf("isHealthyGenerate204(%d, %d) = %v, want %v", c.status, c.bodyLen, got, c.want)
			}
		})
	}
}

// TestBrigadeIsOurPAC checks PAC URL ownership against our HTTP port.
func TestBrigadeIsOurPAC(t *testing.T) {
	old := Config
	defer func() { Config = old }()
	Config = &AppConfig{}
	Config.Network.PACHttpPort = 8080

	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"ours", "http://127.0.0.1:8080/x_proxy.pac", true},
		{"foreign port", "http://127.0.0.1:9999/x_proxy.pac", false},
		{"foreign host", "http://proxy.corp/pac", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isOurPAC(c.url); got != c.want {
				t.Errorf("isOurPAC(%q) = %v, want %v", c.url, got, c.want)
			}
		})
	}
}

// brigadeFreePort returns a currently-free loopback port >= min (if found
// within a few tries), closed before return so the caller can reuse it.
func brigadeFreePort(t *testing.T, min int) int {
	t.Helper()
	fallback := 0
	for i := 0; i < 50; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen for free port: %v", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		if port >= min {
			return port
		}
		fallback = port
	}
	if fallback != 0 {
		return fallback
	}
	t.Fatal("could not find a free port")
	return 0
}

// TestBrigadeProbeTunnelDeadPort probes a certainly-dead SSH target and
// expects failure classified as a network problem.
func TestBrigadeProbeTunnelDeadPort(t *testing.T) {
	closedPort := brigadeFreePort(t, 40000)
	testPort := brigadeFreePort(t, 40000)
	sshCmd := []string{
		"ssh",
		"-D", fmt.Sprintf("127.0.0.1:%d", testPort),
		"-p", fmt.Sprintf("%d", closedPort),
		"-o", "ConnectTimeout=1",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-N", "127.0.0.1",
	}
	ok, reason := probeTunnelWithReason(sshCmd, testPort, 5*time.Second)
	if ok {
		t.Fatalf("probeTunnelWithReason to dead port: ok = true, want false")
	}
	if reason != "net" {
		t.Fatalf("probeTunnelWithReason to dead port: reason = %q, want %q", reason, "net")
	}
}
