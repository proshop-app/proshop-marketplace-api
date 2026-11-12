// Package contextcache реализует ограниченный кэш ценовых контекстов процесса.
package contextcache

import (
	"context"
	"maps"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type record struct {
	entry   ports.PriceContextEntry
	token   string
	expires time.Time
	used    uint64
}

// Memory ограничивает число покупателей, включая незавершённые загрузки.
type Memory struct {
	mu       sync.Mutex
	records  map[ids.UUID]record
	capacity int
	sequence uint64
	clock    ports.Clock
	metrics  ports.PriceContextMetrics
}

// NewMemory создаёт ограниченный резерв с управляемыми часами.
func NewMemory(capacity int, clock ports.Clock, metrics ports.PriceContextMetrics) *Memory {
	return &Memory{
		records:  make(map[ids.UUID]record),
		capacity: capacity,
		clock:    clock,
		metrics:  metrics,
	}
}

// Get очищает истёкшие записи и возвращает копию карты.
func (m *Memory) Get(_ context.Context, buyer ids.UUID) (ports.PriceContextEntry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune()
	value, found := m.records[buyer]
	if !found || value.entry.LoadedAt.IsZero() {
		return ports.PriceContextEntry{}, false, nil
	}
	m.sequence++
	value.used = m.sequence
	m.records[buyer] = value
	value.entry.Profiles = maps.Clone(value.entry.Profiles)
	return value.entry, true, nil
}

// Begin регистрирует загрузку и вытесняет наименее используемого покупателя.
func (m *Memory) Begin(_ context.Context, buyer ids.UUID, token string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune()
	value, found := m.records[buyer]
	if !found && len(m.records) >= m.capacity {
		var oldest ids.UUID
		used := ^uint64(0)
		for id, entry := range m.records {
			if entry.used < used {
				oldest, used = id, entry.used
			}
		}
		delete(m.records, oldest)
	}
	m.sequence++
	value.token = token
	value.used = m.sequence
	value.expires = m.clock.Now().Add(ttl)
	m.records[buyer] = value
	m.size()
	return nil
}

// Store не допускает завершения загрузки после удаления или вытеснения.
func (m *Memory) Store(
	_ context.Context,
	buyer ids.UUID,
	token string,
	entry ports.PriceContextEntry,
	ttl time.Duration,
) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune()
	value, found := m.records[buyer]
	if !found || value.token != token {
		return false, nil
	}
	entry.Profiles = maps.Clone(entry.Profiles)
	value.entry = entry
	value.expires = m.clock.Now().Add(ttl)
	m.records[buyer] = value
	return true, nil
}

// Delete отменяет также незавершённую загрузку.
func (m *Memory) Delete(_ context.Context, buyer ids.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, buyer)
	m.prune()
	return nil
}

func (m *Memory) prune() {
	now := m.clock.Now()
	for buyer, value := range m.records {
		if !now.Before(value.expires) {
			delete(m.records, buyer)
		}
	}
	m.size()
}

func (m *Memory) size() {
	if m.metrics != nil {
		m.metrics.PriceContextSize("memory", len(m.records))
	}
}
