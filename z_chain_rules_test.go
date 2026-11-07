// z_chain_rules_test.go — тесты правил цепочек с недоступными напрямую хостами.
//
// Покрывает чистые функции chain_rules.go без GUI (systray паникует без трея).
package main

import (
	"strings"
	"testing"
	"time"
)

func setupChainCache() {
	hostStatusCache = NewHostStatusCache()
	hostStatusCache.Set("direct-a", true)
	hostStatusCache.Set("direct-b", true)
	hostStatusCache.Set("dead-x", false)
	// "ghost" — без записи в кэше (статус неизвестен).
}

// ---------------------------------------------------------------------------
// classifyChain
// ---------------------------------------------------------------------------

func TestClassifyChain_Empty(t *testing.T) {
	setupChainCache()
	if _, err := classifyChain(nil); err == nil {
		t.Error("empty chain must be rejected")
	}
}

func TestClassifyChain_Single(t *testing.T) {
	setupChainCache()
	// Одиночка проходит структурный гейт при любом статусе —
	// запрет одиночного коннекта срабатывает на клике, а не здесь.
	for _, name := range []string{"direct-a", "dead-x", "ghost"} {
		kind, err := classifyChain([]HostConfig{{Name: name}})
		if err != nil || kind != "single" {
			t.Errorf("single [%s]: got (%q, %v), want (single, nil)", name, kind, err)
		}
	}
}

func TestClassifyChain_Multi(t *testing.T) {
	setupChainCache()
	cases := []struct {
		name    string
		chain   []string
		want    string
		wantErr bool
	}{
		{"direct-first", []string{"direct-a", "dead-x"}, "chain", false},
		{"unknown-first", []string{"ghost", "dead-x"}, "chain", false},
		{"dead-first", []string{"dead-x", "direct-a"}, "", true},
		{"all-dead", []string{"dead-x", "dead-x"}, "", true},
	}
	for _, c := range cases {
		var chain []HostConfig
		for _, n := range c.chain {
			chain = append(chain, HostConfig{Name: n})
		}
		kind, err := classifyChain(chain)
		if kind != c.want || (err != nil) != c.wantErr {
			t.Errorf("%s: got (%q, %v), want (%q, err=%v)", c.name, kind, err, c.want, c.wantErr)
		}
	}
}

// ---------------------------------------------------------------------------
// connectButtonState
// ---------------------------------------------------------------------------

func TestConnectButtonState(t *testing.T) {
	setupChainCache()
	H := func(names ...string) []HostConfig {
		var out []HostConfig
		for _, n := range names {
			out = append(out, HostConfig{Name: n})
		}
		return out
	}

	if en, _, tip := connectButtonState(nil); en {
		t.Error("empty builder: button must be disabled")
	} else if tip != "Select hosts then click to connect" {
		t.Errorf("empty builder: unexpected tooltip %q", tip)
	}

	if en, _, _ := connectButtonState(H("direct-a")); !en {
		t.Error("single reachable: button must be enabled")
	}

	if en, _, tip := connectButtonState(H("dead-x")); en {
		t.Error("single unreachable: button must be disabled")
	} else if !strings.HasPrefix(tip, "Blocked:") {
		t.Errorf("single unreachable: tooltip must explain the block, got %q", tip)
	}

	if en, _, _ := connectButtonState(H("direct-a", "dead-x")); !en {
		t.Error("chain with reachable first hop: button must be enabled")
	}

	if en, _, tip := connectButtonState(H("dead-x", "direct-a")); en {
		t.Error("chain with unreachable first hop: button must be disabled")
	} else if !strings.Contains(tip, "dead-x") {
		t.Errorf("chain with unreachable first hop: tooltip must name the hop, got %q", tip)
	}
}

// ---------------------------------------------------------------------------
// recordSocks (debounce)
// ---------------------------------------------------------------------------

func TestRecordSocks_Threshold(t *testing.T) {
	mc := &monitoringConfig{}
	if mc.recordSocks(false) {
		t.Error("1st fail must not trigger")
	}
	if mc.recordSocks(false) {
		t.Error("2nd fail must not trigger")
	}
	if !mc.recordSocks(false) {
		t.Error("3rd consecutive fail must trigger failover")
	}
	if mc.recordSocks(true); mc.socksFails != 0 {
		t.Error("success must reset the counter")
	}
	if mc.recordSocks(false) {
		t.Error("counter must restart after reset")
	}
}

// ---------------------------------------------------------------------------
// failoverExpired / noInternetShouldBeRed
// ---------------------------------------------------------------------------

func TestFailoverExpired(t *testing.T) {
	now := time.Now()
	if failoverExpired(time.Time{}, now) {
		t.Error("zero start must never expire")
	}
	if failoverExpired(now, now) {
		t.Error("fresh failover must not expire")
	}
	if !failoverExpired(now.Add(-MaxFailoverDuration-time.Minute), now) {
		t.Error("overdue failover must expire")
	}
}

func TestNoInternetShouldBeRed(t *testing.T) {
	now := time.Now()
	if noInternetShouldBeRed(time.Time{}, now) {
		t.Error("zero start must stay yellow")
	}
	if noInternetShouldBeRed(now, now) {
		t.Error("fresh outage must stay yellow (grace)")
	}
	if !noInternetShouldBeRed(now.Add(-NoInternetRedAfter-time.Minute), now) {
		t.Error("persistent outage must turn red")
	}
}

// ---------------------------------------------------------------------------
// Badge builders
// ---------------------------------------------------------------------------

func TestChainOnlyMarkers(t *testing.T) {
	if !strings.HasPrefix(chainOnlyTitle("myhost   "), ChainOnlyMarker) {
		t.Errorf("title must start with %q", ChainOnlyMarker)
	}
	tip := chainOnlyTooltip("myhost")
	if !strings.Contains(tip, "myhost") || !strings.Contains(tip, "chain only") {
		t.Errorf("tooltip must name the host and the chain-only rule, got %q", tip)
	}
}

// ---------------------------------------------------------------------------
// preflightTimeout scaling
// ---------------------------------------------------------------------------

func TestPreflightTimeout_ScalesWithChain(t *testing.T) {
	if d := preflightTimeout(1); d != 15*time.Second {
		t.Errorf("len 1: got %v, want 15s", d)
	}
	if d := preflightTimeout(5); d != 45*time.Second {
		t.Errorf("len 5: got %v, want capped 45s", d)
	}
	if d := preflightTimeout(0); d != 15*time.Second {
		t.Errorf("len 0: got %v, want floor 15s", d)
	}
}

// ---------------------------------------------------------------------------
// firstHopProbeFn without config
// ---------------------------------------------------------------------------

func TestFirstHopProbe_NilConfig(t *testing.T) {
	old := Config
	Config = nil
	defer func() { Config = old }()
	if firstHopProbeFn(HostConfig{Name: "x"}) {
		t.Error("nil config must probe as unreachable")
	}
}

func TestSleepGapExceeded(t *testing.T) {
	now := time.Now()
	if sleepGapExceeded(time.Time{}, now) {
		t.Error("zero last tick must not count as gap")
	}
	if sleepGapExceeded(now, now) {
		t.Error("fresh tick must not count as gap")
	}
	if !sleepGapExceeded(now.Add(-time.Minute), now) {
		t.Error("60s gap must count as sleep/wake")
	}
	if sleepGapExceeded(now.Add(-10*time.Second), now) {
		t.Error("10s gap must not count as sleep/wake")
	}
}
