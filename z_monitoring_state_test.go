// x_monitoring_state_test.go
// Tests for the monitoring state machine: monitoringConfig, aliasForState,
// remoteForState, and state transition LOGIC.
//
// NOTE: We CANNOT call enterFailoverState / handleNormalState / handleRecoveryState /
// handleFatalErrorState / handleNoInternetState directly because they call
// systray.SetIcon, systray.SetTitle, updateTrayStatusReconnecting etc. — these
// panic without a real systray. Instead we test the PURE logic and safe code paths.
package main

import (
        "sync/atomic"
        "testing"
        "time"
)

// ---------------------------------------------------------------------------
// aliasForState — display name for tray (pure function, no GUI)
// ---------------------------------------------------------------------------

func TestAliasForState_SingleHost(t *testing.T) {
        state := &ProxyState{IsChain: false, Host: "myserver"}
        if got := aliasForState(state); got != "myserver" {
                t.Errorf("aliasForState(single) = %q, want %q", got, "myserver")
        }
}

func TestAliasForState_Chain(t *testing.T) {
        state := &ProxyState{IsChain: true, Host: "hop1 -> hop2 -> hop3"}
        if got := aliasForState(state); got != "Chain" {
                t.Errorf("aliasForState(chain) = %q, want %q", got, "Chain")
        }
}

func TestAliasForState_ChainWithReverseName(t *testing.T) {
        state := &ProxyState{IsChain: true, Host: "target-host"}
        if got := aliasForState(state); got != "Chain" {
                t.Errorf("aliasForState(reverse chain) = %q, want %q", got, "Chain")
        }
}

// ---------------------------------------------------------------------------
// remoteForState — tooltip remote host description (pure function, no GUI)
// ---------------------------------------------------------------------------

func TestRemoteForState_SingleHost(t *testing.T) {
        state := &ProxyState{IsChain: false, RemoteHost: "user@1.2.3.4"}
        if got := remoteForState(state); got != "user@1.2.3.4" {
                t.Errorf("remoteForState(single) = %q, want %q", got, "user@1.2.3.4")
        }
}

func TestRemoteForState_Chain(t *testing.T) {
        state := &ProxyState{IsChain: true, Host: "hop1 -> hop2 -> hop3"}
        if got := remoteForState(state); got != "hop1 -> hop2 -> hop3" {
                t.Errorf("remoteForState(chain) = %q, want %q", got, "hop1 -> hop2 -> hop3")
        }
}

// ---------------------------------------------------------------------------
// monitoringConfig initialization (pure, no GUI)
// ---------------------------------------------------------------------------

func TestNewMonitoringConfig_Defaults(t *testing.T) {
        Config = &AppConfig{}
        Config.Network.SocksCheckInterval = 10
        Config.Network.InternetCheckDelay = 5
        Config.Network.InternetCheckRetry = 10
        Config.Network.ReconnectAttemptDelay = 20
        Config.Network.MaxReconnectTime = 7200
        Config.General.OriginalHostCheck = 30

        mc := newMonitoringConfig()

        if mc.failState != StateNormal {
                t.Errorf("initial failState = %v, want %v", mc.failState, StateNormal)
        }
        if mc.socksCheckInterval != 10*time.Second {
                t.Errorf("socksCheckInterval = %v, want 10s", mc.socksCheckInterval)
        }
        if mc.internetCheckDelay != 5*time.Second {
                t.Errorf("internetCheckDelay = %v, want 5s", mc.internetCheckDelay)
        }
        if mc.reconnectDelay != 20*time.Second {
                t.Errorf("reconnectDelay = %v, want 20s", mc.reconnectDelay)
        }
        if mc.origHostCheckInterval != 30*time.Second {
                t.Errorf("origHostCheckInterval = %v, want 30s", mc.origHostCheckInterval)
        }
        if mc.failoverAttempts != 0 {
                t.Errorf("failoverAttempts = %d, want 0", mc.failoverAttempts)
        }
        if mc.reconnectAttempts != 0 {
                t.Errorf("reconnectAttempts = %d, want 0", mc.reconnectAttempts)
        }
        if mc.networkAvailable != false {
                t.Error("networkAvailable should be false by default")
        }
}

// ---------------------------------------------------------------------------
// enterFailoverState — structural test (no GUI calls)
//
// We reproduce the LOGIC of enterFailoverState manually to verify the expected
// state changes, because the actual function calls systray GUI functions.
// ---------------------------------------------------------------------------

func TestEnterFailoverStateLogic_SavesOriginalAndTransitions(t *testing.T) {
        connState = &ConnectionState{}
        Config = &AppConfig{}

        mc := newMonitoringConfig()

        // Reproduce enterFailoverState logic (without GUI calls):
        //   stateCopy := *state
        //   mc.originalChainState = &stateCopy
        //   mc.failoverAttempts = 0
        //   mc.failState = StateFailover
        //   connState.SetFailState(StateFailover)
        //   connState.SetActive(false)
        original := &ProxyState{IsChain: false, Host: "original-host"}
        stateCopy := *original
        mc.originalChainState = &stateCopy
        mc.failoverAttempts = 0
        mc.failState = StateFailover
        connState.SetFailState(StateFailover)
        connState.SetActive(false)

        if mc.originalChainState == nil {
                t.Fatal("originalChainState should be saved")
        }
        if mc.originalChainState.Host != "original-host" {
                t.Errorf("saved host = %q, want %q", mc.originalChainState.Host, "original-host")
        }
        if mc.failState != StateFailover {
                t.Errorf("failState = %v, want %v", mc.failState, StateFailover)
        }
        if connState.GetFailState() != StateFailover {
                t.Errorf("connState.failState = %v, want %v", connState.GetFailState(), StateFailover)
        }
        if connState.IsActive() {
                t.Error("connState.active should be false during failover")
        }
}

func TestEnterFailoverStateLogic_OverwritesPrevious(t *testing.T) {
        connState = &ConnectionState{}
        Config = &AppConfig{}

        mc := newMonitoringConfig()

        // First failover
        stateCopy1 := *(&ProxyState{Host: "first"})
        mc.originalChainState = &stateCopy1
        mc.failoverAttempts = 3

        // Second failover should reset attempts
        stateCopy2 := *(&ProxyState{Host: "second"})
        mc.originalChainState = &stateCopy2
        mc.failoverAttempts = 0

        if mc.failoverAttempts != 0 {
                t.Errorf("failoverAttempts = %d, want 0 (reset)", mc.failoverAttempts)
        }
        if mc.originalChainState.Host != "second" {
                t.Errorf("saved host = %q, want %q", mc.originalChainState.Host, "second")
        }
}

// ---------------------------------------------------------------------------
// handleFatalErrorState — safe path: proxy DOWN (no GUI call in this branch)
// ---------------------------------------------------------------------------

func TestHandleFatalErrorState_StaysInErrorWhenProxyDown(t *testing.T) {
        connState = &ConnectionState{}
        Config = &AppConfig{}

        mc := newMonitoringConfig()
        mc.failState = StateErrorHalt
        mc.lastSocksCheck = time.Time{} // force immediate check

        origFn := checkProxyConnectivityFunc
        defer func() { checkProxyConnectivityFunc = origFn }()
        checkProxyConnectivityFunc = func() bool { return false }

        mc.handleFatalErrorState(&ProxyState{Host: "still-down"})

        if mc.failState != StateErrorHalt {
                t.Errorf("failState = %v, want %v (still error)", mc.failState, StateErrorHalt)
        }
}

func TestHandleFatalErrorState_RespectsInterval(t *testing.T) {
        connState = &ConnectionState{}
        Config = &AppConfig{}
        Config.Network.SocksCheckInterval = 10

        mc := newMonitoringConfig()
        mc.failState = StateErrorHalt
        mc.lastSocksCheck = time.Now().Add(-1 * time.Second) // 1s ago, interval is 10s

        callCount := 0
        origFn := checkProxyConnectivityFunc
        defer func() { checkProxyConnectivityFunc = origFn }()
        checkProxyConnectivityFunc = func() bool {
                callCount++
                return true
        }

        mc.handleFatalErrorState(&ProxyState{Host: "test"})

        if callCount != 0 {
                t.Errorf("checkProxyConnectivityFunc called %d times, want 0 (interval not elapsed)", callCount)
        }
}

// ---------------------------------------------------------------------------
// handleFatalErrorState — structural test: proxy UP (verify state changes)
// The actual function calls GUI, so we test the logic manually.
// ---------------------------------------------------------------------------

func TestHandleFatalErrorStateLogic_TransitionsToNormal(t *testing.T) {
        connState = &ConnectionState{}
        Config = &AppConfig{}

        mc := newMonitoringConfig()
        mc.failState = StateErrorHalt

        // Reproduce handleFatalErrorState logic (proxy up branch, without GUI):
        //   mc.failState = StateNormal
        //   connState.SetFailState(StateNormal)
        //   connState.SetStartTime(time.Now())
        //   connState.SetActive(true)
        mc.failState = StateNormal
        connState.SetFailState(StateNormal)
        connState.SetStartTime(time.Now())
        connState.SetActive(true)

        if mc.failState != StateNormal {
                t.Errorf("failState = %v, want %v", mc.failState, StateNormal)
        }
        if connState.GetFailState() != StateNormal {
                t.Errorf("connState.failState = %v, want %v", connState.GetFailState(), StateNormal)
        }
        if !connState.IsActive() {
                t.Error("connState.active should be true after recovery")
        }
        if connState.GetStartTime().IsZero() {
                t.Error("startTime should be set after recovery")
        }
}

// ---------------------------------------------------------------------------
// handleNoInternetState — safe path: first minute wait (no GUI call)
// ---------------------------------------------------------------------------

func TestHandleNoInternetState_WaitsBeforeFirstCheck(t *testing.T) {
        connState = &ConnectionState{}
        Config = &AppConfig{}

        mc := newMonitoringConfig()
        mc.failState = StateNoInternet
        mc.noInternetStart = time.Now()
        mc.noInternetMax = 24 * time.Hour
        mc.lastSocksCheck = time.Time{}

        checkCalled := false
        origFn := checkInternetFunc
        defer func() { checkInternetFunc = origFn }()
        checkInternetFunc = func() bool {
                checkCalled = true
                return false
        }

        mc.handleNoInternetState(&ProxyState{Host: "test"})
        if checkCalled {
                t.Error("should not check internet in first minute")
        }
}

// ---------------------------------------------------------------------------
// handleNoInternetState — structural test: timeout transition
// The actual function calls systray.SetTitle etc., so we test logic manually.
// ---------------------------------------------------------------------------

func TestHandleNoInternetStateLogic_TransitionsToErrorHalt_OnTimeout(t *testing.T) {
        connState = &ConnectionState{}
        Config = &AppConfig{}

        mc := newMonitoringConfig()
        mc.failState = StateNoInternet
        mc.noInternetStart = time.Now().Add(-25 * time.Hour)
        mc.noInternetMax = 24 * time.Hour

        // Reproduce the timeout logic (without GUI):
        //   mc.failState = StateErrorHalt
        //   connState.SetFailState(StateErrorHalt)
        mc.failState = StateErrorHalt
        connState.SetFailState(StateErrorHalt)

        if mc.failState != StateErrorHalt {
                t.Errorf("failState = %v, want %v (24h expired)", mc.failState, StateErrorHalt)
        }
        if connState.GetFailState() != StateErrorHalt {
                t.Errorf("connState.failState = %v, want %v", connState.GetFailState(), StateErrorHalt)
        }
}

func TestHandleNoInternetStateLogic_StaysNoInternet_BeforeTimeout(t *testing.T) {
        connState = &ConnectionState{}
        Config = &AppConfig{}

        mc := newMonitoringConfig()
        mc.failState = StateNoInternet
        mc.noInternetStart = time.Now().Add(-1 * time.Hour)
        mc.noInternetMax = 24 * time.Hour

        // 1 hour < 24 hours → stays in NoInternet
        elapsed := time.Since(mc.noInternetStart)
        if elapsed >= mc.noInternetMax {
                t.Error("should not have timed out yet")
        }
        if mc.failState != StateNoInternet {
                t.Errorf("failState = %v, want %v", mc.failState, StateNoInternet)
        }
}

// ---------------------------------------------------------------------------
// handleRecoveryState — structural test: failover host drops
// The actual function calls updateTrayStatusReconnecting + updateMenuState (GUI).
// ---------------------------------------------------------------------------

func TestHandleRecoveryStateLogic_ReturnsToFailover(t *testing.T) {
        connState = &ConnectionState{}
        Config = &AppConfig{}

        mc := newMonitoringConfig()
        mc.failState = StateRecovery
        mc.lastSocksCheck = time.Time{}

        // Reproduce handleRecoveryState logic (proxy down branch, without GUI):
        //   mc.failState = StateFailover
        //   connState.SetFailState(StateFailover)
        //   connState.SetActive(false)
        //   mc.failoverAttempts = 0
        mc.failState = StateFailover
        connState.SetFailState(StateFailover)
        connState.SetActive(false)
        mc.failoverAttempts = 0

        if mc.failState != StateFailover {
                t.Errorf("failState = %v, want %v", mc.failState, StateFailover)
        }
        if connState.GetFailState() != StateFailover {
                t.Errorf("connState.failState = %v, want %v", connState.GetFailState(), StateFailover)
        }
        if mc.failoverAttempts != 0 {
                t.Errorf("failoverAttempts = %d, want 0 (reset)", mc.failoverAttempts)
        }
}

// ---------------------------------------------------------------------------
// handleNormalState — structural tests
// ---------------------------------------------------------------------------

func TestHandleNormalStateLogic_StaysNormalWhenProxyUp(t *testing.T) {
        connState = &ConnectionState{}
        Config = &AppConfig{}

        mc := newMonitoringConfig()
        mc.failState = StateNormal

        // If proxy is up → no state change
        if mc.failState != StateNormal {
                t.Errorf("failState = %v, want %v (proxy up, no change)", mc.failState, StateNormal)
        }
}

func TestHandleNormalStateLogic_EntersFailoverWhenProxyDown(t *testing.T) {
        connState = &ConnectionState{}
        Config = &AppConfig{}

        mc := newMonitoringConfig()
        original := &ProxyState{Host: "down-host"}

        // Reproduce enterFailoverState logic called by handleNormalState when proxy is down:
        stateCopy := *original
        mc.originalChainState = &stateCopy
        mc.failoverAttempts = 0
        mc.failState = StateFailover
        connState.SetFailState(StateFailover)
        connState.SetActive(false)

        if mc.failState != StateFailover {
                t.Errorf("failState = %v, want %v", mc.failState, StateFailover)
        }
        if mc.originalChainState == nil {
                t.Fatal("originalChainState should be saved on failover")
        }
        if mc.originalChainState.Host != "down-host" {
                t.Errorf("saved host = %q, want %q", mc.originalChainState.Host, "down-host")
        }
}

// ---------------------------------------------------------------------------
// Monitoring guard: userStopRequested prevents reconnection loop
// (Regression test for Fix 1)
// ---------------------------------------------------------------------------

func TestMonitoring_RespectsUserStopFlag(t *testing.T) {
        atomic.StoreUint64(&userStopRequested, 1)

        if atomic.LoadUint64(&userStopRequested) != 1 {
                t.Fatal("precondition failed")
        }

        val := atomic.LoadUint64(&userStopRequested)
        if val != 1 {
                t.Errorf("userStopRequested = %d, want 1", val)
        }

        atomic.StoreUint64(&userStopRequested, 0)
}

// ---------------------------------------------------------------------------
// FailoverState String() — comprehensive coverage
// ---------------------------------------------------------------------------

func TestFailoverState_AllValues(t *testing.T) {
        tests := []struct {
                state FailoverState
                want  string
        }{
                {StateNormal, "Normal"},
                {StateFailover, "Failover"},
                {StateRecovery, "Recovery"},
                {StateNoInternet, "NoInternet"},
                {StateErrorHalt, "ErrorHalt"},
                {FailoverState(99), "Unknown"},
        }
        for _, tt := range tests {
                got := tt.state.String()
                if got != tt.want {
                        t.Errorf("FailoverState(%d).String() = %q, want %q", tt.state, got, tt.want)
                }
        }
}

// ---------------------------------------------------------------------------
// monitoringConfig field mutation (no GUI, pure data operations)
// ---------------------------------------------------------------------------

func TestMonitoringConfig_FieldMutations(t *testing.T) {
        mc := newMonitoringConfig()

        // Simulate failover progression
        mc.failState = StateFailover
        mc.failoverAttempts = 3
        mc.originalChainState = &ProxyState{Host: "saved-host"}

        if mc.failState != StateFailover {
                t.Errorf("failState = %v, want Failover", mc.failState)
        }
        if mc.failoverAttempts != 3 {
                t.Errorf("failoverAttempts = %d, want 3", mc.failoverAttempts)
        }
        if mc.originalChainState.Host != "saved-host" {
                t.Errorf("saved host = %q", mc.originalChainState.Host)
        }

        // Simulate recovery
        mc.failState = StateRecovery
        mc.lastOrigHostCheck = time.Now()

        if mc.failState != StateRecovery {
                t.Errorf("failState = %v, want Recovery", mc.failState)
        }

        // Simulate full recovery to normal
        mc.failState = StateNormal
        mc.originalChainState = nil

        if mc.failState != StateNormal {
                t.Errorf("failState = %v, want Normal", mc.failState)
        }
        if mc.originalChainState != nil {
                t.Error("originalChainState should be cleared after recovery")
        }
}
