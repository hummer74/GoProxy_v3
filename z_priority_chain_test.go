// z_priority_chain_test.go
// P1 regression tests (Oct 2026): the user-selected target is stored
// pipe-separated ("name" or "a|b|c") while a live chain displays
// arrow-separated ("a -> b"). Direct string comparison between the two
// formats can never match, which broke (a) the Normal-state priority
// return for chains and (b) the failover exclusion of failed chain
// members (e.g. failover 05-DE -> 05-22DE: same host, other port).
// All helpers are pure — no Windows, no SSH, no tray.
package main

import (
	"testing"
)

func TestSplitPriorityNames(t *testing.T) {
	if got := splitPriorityNames("solo"); len(got) != 1 || got[0] != "solo" {
		t.Fatalf("single = %v, want [solo]", got)
	}
	got := splitPriorityNames("a|b|c")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("chain = %v, want [a b c]", got)
	}
}

func TestPriorityMatchesState(t *testing.T) {
	single := &ProxyState{IsChain: false, Host: "05-DE"}
	if !priorityMatchesState(single, "05-DE") {
		t.Error("single match should be true")
	}
	if priorityMatchesState(single, "02-NL") {
		t.Error("single mismatch should be false")
	}
	chain := &ProxyState{IsChain: true, Host: "a -> b", ChainHosts: []string{"a", "b"}}
	if !priorityMatchesState(chain, "a|b") {
		t.Error("chain match should be true despite different separators")
	}
	if priorityMatchesState(chain, "a|c") {
		t.Error("chain member mismatch should be false")
	}
	if priorityMatchesState(chain, "a|b|c") {
		t.Error("chain length mismatch should be false")
	}
	if priorityMatchesState(chain, "a -> b") {
		t.Error("arrow-format priority must not match (canonical is pipe)")
	}
	if priorityMatchesState(single, "a|b") {
		t.Error("single state must not match chain priority")
	}
	if priorityMatchesState(nil, "x") || priorityMatchesState(single, "") {
		t.Error("nil state / empty priority must not match")
	}
}

func TestBuildFailoverExcludeSet(t *testing.T) {
	if got := buildFailoverExcludeSet(nil); len(got) != 0 {
		t.Fatalf("nil = %v, want empty", got)
	}
	single := buildFailoverExcludeSet(&ProxyState{Host: "05-DE", OriginalHost: "05-DE"})
	if len(single) != 1 || !single["05-DE"] {
		t.Fatalf("single = %v, want {05-DE}", single)
	}
	chain := buildFailoverExcludeSet(&ProxyState{
		Host: "a -> b", OriginalHost: "a -> b", ChainHosts: []string{"a", "b"},
	})
	if len(chain) != 2 || !chain["a"] || !chain["b"] {
		t.Fatalf("chain = %v, want {a b} (display string alone must not leak in)", chain)
	}
	if chain["a -> b"] {
		t.Error("display string must never be a candidate name")
	}
}

func TestRestoreOriginalFromDisk(t *testing.T) {
	if restoreOriginalFromDisk(nil) != nil {
		t.Fatal("nil must restore nil")
	}
	plain := &ProxyState{IsChain: false, Host: "02-NL"}
	if restoreOriginalFromDisk(plain) != nil {
		t.Fatal("non-failover state must restore nil")
	}
	single := &ProxyState{IsChain: false, Host: "02-NL", OriginalHost: "05-DE", IsFailoverActive: true}
	got := restoreOriginalFromDisk(single)
	if got == nil || got.IsChain || got.Host != "05-DE" || got.OriginalHost != "05-DE" {
		t.Fatalf("single restore = %+v, want Host=05-DE", got)
	}
	chainFO := &ProxyState{IsChain: false, Host: "02-NL", OriginalHost: "a -> b", IsFailoverActive: true}
	got = restoreOriginalFromDisk(chainFO)
	if got == nil || !got.IsChain || len(got.ChainHosts) != 2 || got.ChainHosts[0] != "a" || got.ChainHosts[1] != "b" {
		t.Fatalf("chain restore = %+v, want ChainHosts=[a b]", got)
	}
}
