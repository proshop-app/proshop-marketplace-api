package fake

import (
	"context"
	"maps"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// PriceContextCache позволяет проверять сбои и отмену загрузки без Redis.
type PriceContextCache struct {
	GetError    error
	DeleteError error
	mu          sync.Mutex
	entries     map[ids.UUID]ports.PriceContextEntry
	tokens      map[ids.UUID]string
}

// PriceContextMetrics уведомляет тест о завершении работы без опроса состояния и задержек.
type PriceContextMetrics struct {
	Events chan string
	mu     sync.Mutex
	counts map[string]int
}

// PriceContextPreloader сохраняет запрошенных при входе покупателей.
type PriceContextPreloader struct {
	Buyers []ids.UUID
}

// Preload записывает покупателя для проверки сценария входа.
func (p *PriceContextPreloader) Preload(buyer ids.UUID) {
	p.Buyers = append(p.Buyers, buyer)
}

// Get возвращает независимую копию записи и настроенную ошибку.
func (c *PriceContextCache) Get(_ context.Context, buyer ids.UUID) (ports.PriceContextEntry, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.entries[buyer]
	entry.Profiles = maps.Clone(entry.Profiles)
	return entry, found, c.GetError
}

// Begin запоминает токен новой загрузки.
func (c *PriceContextCache) Begin(
	_ context.Context,
	buyer ids.UUID,
	token string,
	_ time.Duration,
) (ports.PriceContextEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokens == nil {
		c.tokens = make(map[ids.UUID]string)
	}
	c.tokens[buyer] = token
	entry := c.entries[buyer]
	entry.Profiles = maps.Clone(entry.Profiles)
	return entry, nil
}

// Store сохраняет запись только с актуальным токеном.
func (c *PriceContextCache) Store(
	_ context.Context,
	buyer ids.UUID,
	token string,
	entry ports.PriceContextEntry,
	_ time.Duration,
) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokens[buyer] != token {
		return false, nil
	}
	if c.entries == nil {
		c.entries = make(map[ids.UUID]ports.PriceContextEntry)
	}
	entry.Profiles = maps.Clone(entry.Profiles)
	c.entries[buyer] = entry
	return true, nil
}

// Delete удаляет запись и отменяет её загрузку.
func (c *PriceContextCache) Delete(_ context.Context, buyer ids.UUID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.DeleteError != nil {
		return c.DeleteError
	}
	delete(c.tokens, buyer)
	delete(c.entries, buyer)
	return nil
}

// PriceContext учитывает исход и уведомляет ожидающий тест.
func (m *PriceContextMetrics) PriceContext(operation, outcome string, _ time.Duration) {
	key := operation + ":" + outcome
	m.mu.Lock()
	if m.counts == nil {
		m.counts = make(map[string]int)
	}
	m.counts[key]++
	m.mu.Unlock()
	if m.Events != nil {
		m.Events <- key
	}
}

// PriceContextSize не собирает размер в сценарных тестах.
func (m *PriceContextMetrics) PriceContextSize(_ string, _ int) {}

// Count возвращает число наблюдений исхода.
func (m *PriceContextMetrics) Count(operation, outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts[operation+":"+outcome]
}

// PriceContextInvalidation сохраняет исход события в общей истории теста.
func (m *PriceContextMetrics) PriceContextInvalidation(_ ids.TenantID, outcome string) {
	m.PriceContext("event", outcome, 0)
}
