// connection_state.go
package main

import (
	"sync"
	"time"
)

// ConnectionState provides thread-safe access to the current tunnel connection state.
// All three fields (host, active, startTime) are always updated together under a write lock
// so that readers see a consistent snapshot.
type ConnectionState struct {
	mu      sync.RWMutex
	host    string
	active  bool
	started time.Time
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

// GetAll returns a consistent snapshot of all three fields (thread-safe read).
func (c *ConnectionState) GetAll() (host string, active bool, started time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.host, c.active, c.started
}
