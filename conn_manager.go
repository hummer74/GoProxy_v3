// conn_manager.go — единый менеджер соединений.
// Единственная точка истины для поколений сессий: каждая новая сессия
// инвалидирует старые retry/monitor циклы. Проверки идут только через
// connMgr.generation(), legacy-зеркал больше нет.
package main

import (
	"context"
	"sync"
	"sync/atomic"
)

type connectionManager struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	gen    uint64 // инкремент на каждую новую сессию / kill
}

var connMgr = &connectionManager{}

// newSession отменяет предыдущую сессию и возвращает gen + ctx новой.
// Вызывается в начале каждого establishConnection. Мониторинг, запустивший
// коннект изнутри собственного тика, ДОЛЖЕН принять gen (adopt), иначе
// следующий тик сочтёт себя superseded и молча выйдет.
func (m *connectionManager) newSession() (uint64, context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	g := atomic.AddUint64(&m.gen, 1)
	return g, ctx
}

// nextGen инвалидирует running retry-циклы без создания сессии (kill).
func (m *connectionManager) nextGen() uint64 {
	return atomic.AddUint64(&m.gen, 1)
}

func (m *connectionManager) generation() uint64 {
	return atomic.LoadUint64(&m.gen)
}

// stop отменяет текущую сессию (мониторинг/retry увидят ctx.Done / смену gen).
func (m *connectionManager) stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}
