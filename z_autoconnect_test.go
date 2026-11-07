package main

import "testing"

// autoConnectCandidates: pure filter, no network.
func TestAutoConnectCandidates(t *testing.T) {
	if got := autoConnectCandidates(nil, ""); len(got) != 0 {
		t.Fatalf("nil in: got %d, want 0", len(got))
	}
	hosts := []HostConfig{
		{Name: "direct1", Group: "SSH servers"},
		{Name: "direct2", Group: "SSH servers", ProxyJump: ""},
		{Name: "jumped", Group: "SSH servers", ProxyJump: "z-JUMPER"},
		{Name: "rev", Group: "REVERSE Tunnel"},
		{Name: "other", Group: "OTHER"},
	}
	got := autoConnectCandidates(hosts, "direct2")
	if len(got) != 1 || got[0].Name != "direct1" {
		names := []string{}
		for _, h := range got {
			names = append(names, h.Name)
		}
		t.Fatalf("got %v, want [direct1]", names)
	}
	if got := autoConnectCandidates(hosts, ""); len(got) != 2 {
		t.Fatalf("no exclude: got %d, want 2", len(got))
	}
}
