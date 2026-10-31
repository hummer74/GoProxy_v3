// z_monitoring_gen_test.go
// Regression test for the "silent monitoring death" bug (Oct 2026):
// establishConnection() always creates a new connMgr session (gen++).
// Failover/Recovery paths call it from INSIDE their own monitoring tick with
// StopMonitoring:false + StartMonitoring:false. Before the fix, the loop kept
// its stale myGeneration, saw a mismatch on the next tick and exited —
// leaving the failover tunnel unmonitored forever (no return-to-original).
// Observed in prod logs: 2026-10-09 02:03 "Failed to connect (attempt 1)"
// followed by 50 minutes of silence with no "Monitoring stopped".
//
// Fix (variant A): handlers return the new session gen (0 = none created)
// and startMonitoring adopts it. We test the manager mechanics + adopt
// pattern directly (no Windows, no SSH, no tray).
package main

import (
	"testing"
)

// New sessions must bump the generation monotonically and generation()
// must always report the latest one.
func TestConnMgrNewSessionBumpsGen(t *testing.T) {
	g1, _ := connMgr.newSession()
	g2, _ := connMgr.newSession()
	if g2 <= g1 {
		t.Fatalf("newSession did not bump gen: g1=%d g2=%d", g1, g2)
	}
	if cur := connMgr.generation(); cur != g2 {
		t.Fatalf("generation() = %d, want latest %d", cur, g2)
	}
}

// nextGen (kill path) must invalidate running sessions the same way.
func TestConnMgrNextGenInvalidates(t *testing.T) {
	g1, _ := connMgr.newSession()
	myGen := g1
	g2 := connMgr.nextGen()
	if g2 <= myGen {
		t.Fatalf("nextGen did not bump gen: my=%d next=%d", myGen, g2)
	}
	if connMgr.generation() == myGen {
		t.Fatal("stale myGeneration still matches after nextGen — zombie loops would survive")
	}
}

// The core regression: without adoption, a session created inside the
// monitor's own tick looks "superseded" on the next tick and the loop
// exits. With adoption, it survives.
func TestMonitoringAdoptsOwnSessionGen(t *testing.T) {
	g0, _ := connMgr.newSession()
	myGeneration := g0 // startMonitoring captures this once

	// Simulate: handleFailoverState -> establishConnection -> newSession.
	newGen, _ := connMgr.newSession()

	// Old (buggy) behavior: keep stale gen → next tick exits.
	if connMgr.generation() == myGeneration {
		t.Fatal("precondition broken: generations should differ after in-tick newSession")
	}
	exitsWithoutAdopt := connMgr.generation() != myGeneration
	if !exitsWithoutAdopt {
		t.Fatal("expected stale generation to look superseded")
	}

	// Fixed behavior: adopt the returned gen → next tick survives.
	myGeneration = newGen
	if connMgr.generation() != myGeneration {
		t.Fatalf("adopted gen %d does not match current %d", myGeneration, connMgr.generation())
	}
}

// A failed establishConnection still created a session (newSession runs
// first), so handlers must adopt even on failure — otherwise the loop
// dies after the first failed failover attempt (the 02:03 prod case).
func TestMonitoringAdoptsGenOnFailureToo(t *testing.T) {
	_, _ = connMgr.newSession()
	myGeneration := connMgr.generation()

	failedGen, _ := connMgr.newSession() // session bumped, tunnel then failed
	adopt := failedGen                   // handler returns gen despite result == nil
	myGeneration = adopt
	if connMgr.generation() != myGeneration {
		t.Fatal("monitor would exit after failed attempt without adopting failure gen")
	}
}

// Orphan-loop guard: only the current generation may clear monitoringActive.
// Reproduces the startMonitoring defer logic without goroutines.
func TestMonitoringDeferClearsOnlyCurrentGen(t *testing.T) {
	gOld, _ := connMgr.newSession()
	gNew, _ := connMgr.newSession()

	// Old loop exits after a user click started gNew: must NOT clear.
	active := true
	if myGeneration := gOld; connMgr.generation() == myGeneration {
		active = false
	}
	if !active {
		t.Fatal("orphaned loop cleared a newer session's active flag")
	}

	// Current loop exits normally: must clear.
	active = true
	if myGeneration := gNew; connMgr.generation() == myGeneration {
		active = false
	}
	if active {
		t.Fatal("current loop did not clear its own active flag")
	}
}

// stop() must cancel the session context so in-flight waits can abort.
func TestConnMgrStopCancelsContext(t *testing.T) {
	_, ctx := connMgr.newSession()
	connMgr.stop()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("stop() did not cancel the session context")
	}
}
