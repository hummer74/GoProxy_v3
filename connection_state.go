// connection_state.go
package main

import (
        "sync"
        "time"
)

// FailoverState represents the current failover/recovery state of the connection.
type FailoverState int

const (
        StateNormal    FailoverState = iota // All hosts reachable, tunnel active on primary path
        StateFailover                       // Host failed, switching to fastest available host
        StateRecovery                       // On failover host, checking if original chain can be restored
        StateNoInternet                     // No internet connectivity — waiting for network to return
        StateErrorHalt                      // Critical unrecoverable error, system halted
)

// String returns a human-readable state name for logging.
func (s FailoverState) String() string {
        switch s {
        case StateNormal:
                return "Normal"
        case StateFailover:
                return "Failover"
        case StateRecovery:
                return "Recovery"
        case StateNoInternet:
                return "NoInternet"
        case StateErrorHalt:
                return "ErrorHalt"
        default:
                return "Unknown"
        }
}

// ConnectionState provides thread-safe access to the current tunnel connection state.
// All fields are always updated together under a write lock so that readers see a consistent snapshot.
type ConnectionState struct {
        mu        sync.RWMutex
        host      string
        active    bool
        started   time.Time
        failState FailoverState
}

// connState is the single global instance of ConnectionState.
var connState = &ConnectionState{}

// SetConnected atomically sets the tunnel to connected state.
func (c *ConnectionState) SetConnected(host string) {
        c.mu.Lock()
        defer c.mu.Unlock()
        c.host = host
        c.active = true
        c.started = time.Now()
}

// SetDisconnected atomically resets the tunnel to disconnected state.
func (c *ConnectionState) SetDisconnected() {
        c.mu.Lock()
        defer c.mu.Unlock()
        c.host = ""
        c.active = false
        c.started = time.Time{}
}

// SetStartTime atomically updates the tunnel start time (e.g. after reconnection).
func (c *ConnectionState) SetStartTime(t time.Time) {
        c.mu.Lock()
        defer c.mu.Unlock()
        c.started = t
}

// SetActive atomically sets the tunnel active flag.
func (c *ConnectionState) SetActive(active bool) {
        c.mu.Lock()
        defer c.mu.Unlock()
        c.active = active
}

// SetFailState atomically sets the failover state.
func (c *ConnectionState) SetFailState(state FailoverState) {
        c.mu.Lock()
        defer c.mu.Unlock()
        c.failState = state
}

// GetHost returns the currently connected host name (thread-safe read).
func (c *ConnectionState) GetHost() string {
        c.mu.RLock()
        defer c.mu.RUnlock()
        return c.host
}

// IsActive returns whether the tunnel is active (thread-safe read).
func (c *ConnectionState) IsActive() bool {
        c.mu.RLock()
        defer c.mu.RUnlock()
        return c.active
}

// GetStartTime returns the tunnel start time (thread-safe read).
func (c *ConnectionState) GetStartTime() time.Time {
        c.mu.RLock()
        defer c.mu.RUnlock()
        return c.started
}

// GetFailState returns the current failover state (thread-safe read).
func (c *ConnectionState) GetFailState() FailoverState {
        c.mu.RLock()
        defer c.mu.RUnlock()
        return c.failState
}

// GetAll returns a consistent snapshot of all fields (thread-safe read).
func (c *ConnectionState) GetAll() (host string, active bool, started time.Time, failState FailoverState) {
        c.mu.RLock()
        defer c.mu.RUnlock()
        return c.host, c.active, c.started, c.failState
}
