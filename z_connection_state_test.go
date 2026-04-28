// connection_state_test.go
// Tests for ConnectionState thread-safe access and FailoverState enum.
package main

import (
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// FailoverState
// ---------------------------------------------------------------------------

func TestFailoverState_String(t *testing.T) {
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
// ConnectionState — basic get/set
// ---------------------------------------------------------------------------

func resetConnState() {
	connState = &ConnectionState{}
}

func TestConnectionState_SetConnected(t *testing.T) {
	resetConnState()
	connState.SetConnected("myhost")

	if h := connState.GetHost(); h != "myhost" {
		t.Errorf("GetHost() = %q, want %q", h, "myhost")
	}
	if !connState.IsActive() {
		t.Error("IsActive() = false, want true")
	}
	if connState.GetStartTime().IsZero() {
		t.Error("GetStartTime() is zero, want non-zero")
	}
}

func TestConnectionState_SetDisconnected(t *testing.T) {
	resetConnState()
	connState.SetConnected("host1")
	connState.SetDisconnected()

	if h := connState.GetHost(); h != "" {
		t.Errorf("GetHost() = %q, want empty", h)
	}
	if connState.IsActive() {
		t.Error("IsActive() = true, want false")
	}
	if !connState.GetStartTime().IsZero() {
		t.Error("GetStartTime() is non-zero after disconnect, want zero")
	}
}

func TestConnectionState_SetFailState(t *testing.T) {
	resetConnState()

	states := []FailoverState{StateNormal, StateFailover, StateRecovery, StateNoInternet, StateErrorHalt}
	for _, want := range states {
		connState.SetFailState(want)
		if got := connState.GetFailState(); got != want {
			t.Errorf("GetFailState() = %v, want %v", got, want)
		}
	}
}

func TestConnectionState_SetActive(t *testing.T) {
	resetConnState()

	connState.SetActive(true)
	if !connState.IsActive() {
		t.Error("IsActive() = false after SetActive(true)")
	}

	connState.SetActive(false)
	if connState.IsActive() {
		t.Error("IsActive() = true after SetActive(false)")
	}
}

func TestConnectionState_SetStartTime(t *testing.T) {
	resetConnState()

	now := time.Now().Truncate(time.Millisecond) // truncate for comparison
	connState.SetStartTime(now)
	if got := connState.GetStartTime(); !got.Equal(now) {
		t.Errorf("GetStartTime() = %v, want %v", got, now)
	}
}

func TestConnectionState_GetAll(t *testing.T) {
	resetConnState()
	now := time.Now()
	connState.mu.Lock()
	connState.host = "testhost"
	connState.active = true
	connState.started = now
	connState.failState = StateRecovery
	connState.mu.Unlock()

	host, active, started, failState := connState.GetAll()
	if host != "testhost" {
		t.Errorf("GetAll().host = %q, want %q", host, "testhost")
	}
	if !active {
		t.Error("GetAll().active = false, want true")
	}
	if !started.Equal(now) {
		t.Errorf("GetAll().started = %v, want %v", started, now)
	}
	if failState != StateRecovery {
		t.Errorf("GetAll().failState = %v, want %v", failState, StateRecovery)
	}
}

// ---------------------------------------------------------------------------
// ConnectionState — concurrent access (race detector)
// ---------------------------------------------------------------------------

func TestConnectionState_ConcurrentReadWrite(t *testing.T) {
	resetConnState()

	var wg sync.WaitGroup
	// 10 writers, 10 readers, run for 50ms
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				connState.SetConnected("writer")
				connState.SetDisconnected()
				connState.SetFailState(FailoverState(j % 5))
				connState.SetActive(j%2 == 0)
				connState.SetStartTime(time.Now())
			}
		}(i)

		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				connState.GetHost()
				connState.IsActive()
				connState.GetFailState()
				connState.GetStartTime()
				connState.GetAll()
			}
		}(i)
	}
	wg.Wait()
}
