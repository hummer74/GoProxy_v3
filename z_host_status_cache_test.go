// host_status_cache_test.go
// Tests for HostStatusCache — thread-safe status cache for menu hosts.
package main

import (
	"sync"
	"testing"
)

func TestHostStatusCache_SetAndGet(t *testing.T) {
	cache := NewHostStatusCache()

	// Non-existent key
	val, exists := cache.Get("missing")
	if exists {
		t.Error("Get(missing): exists=true, want false")
	}

	// Set and get
	cache.Set("host-a", true)
	val, exists = cache.Get("host-a")
	if !exists {
		t.Error("Get(host-a): exists=false, want true")
	}
	if !val {
		t.Error("Get(host-a): val=false, want true")
	}

	// Update value
	cache.Set("host-a", false)
	val, exists = cache.Get("host-a")
	if !exists || val {
		t.Error("after update: want exists=true, val=false")
	}

	// Multiple hosts
	cache.Set("host-b", true)
	cache.Set("host-c", false)

	val, _ = cache.Get("host-b")
	if !val {
		t.Error("host-b: want true")
	}
	val, _ = cache.Get("host-c")
	if val {
		t.Error("host-c: want false")
	}
}

func TestHostStatusCache_GetAll(t *testing.T) {
	cache := NewHostStatusCache()

	cache.Set("h1", true)
	cache.Set("h2", false)
	cache.Set("h3", true)

	all := cache.GetAll()
	if len(all) != 3 {
		t.Errorf("GetAll() returned %d entries, want 3", len(all))
	}
	if !all["h1"] || all["h2"] || !all["h3"] {
		t.Errorf("GetAll() = %v, want {h1:true, h2:false, h3:true}", all)
	}
}

func TestHostStatusCache_Clear(t *testing.T) {
	cache := NewHostStatusCache()
	cache.Set("h1", true)
	cache.Set("h2", false)

	cache.Clear()

	_, exists := cache.Get("h1")
	if exists {
		t.Error("after Clear: h1 still exists")
	}
	all := cache.GetAll()
	if len(all) != 0 {
		t.Errorf("after Clear: GetAll() has %d entries, want 0", len(all))
	}
}

func TestHostStatusCache_ConcurrentAccess(t *testing.T) {
	cache := NewHostStatusCache()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(3)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				cache.Set("key", id%2 == 0)
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				cache.Get("key")
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				cache.GetAll()
			}
		}()
	}
	wg.Wait()
}
