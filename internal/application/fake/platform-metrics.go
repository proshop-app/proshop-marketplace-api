package fake

import (
	"context"
	"sync"
	"time"
)

// PlatformMetrics сохраняет исходы без подключения Prometheus.
type PlatformMetrics struct {
	mu       sync.Mutex
	outcomes map[string]int
	queues   map[string]int
}

// PlatformCall потокобезопасно учитывает исход операции.
func (m *PlatformMetrics) PlatformCall(operation, class, outcome string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.outcomes == nil {
		m.outcomes = make(map[string]int)
	}
	m.outcomes[operation+":"+outcome]++
	m.outcomes[operation+":"+class+":"+outcome]++
}

// Count возвращает число наблюдений исхода.
func (m *PlatformMetrics) Count(operation, outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.outcomes[operation+":"+outcome]
}

// CountClass возвращает число исходов операции в выбранном вызывающим классе.
func (m *PlatformMetrics) CountClass(operation, class, outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.outcomes[operation+":"+class+":"+outcome]
}

func (p *Platform) platformCall(ctx context.Context, operation string) error {
	if p.CallHook != nil {
		return p.CallHook(ctx, operation)
	}
	return ctx.Err()
}

// PlatformQueueWait учитывает ожидание отдельно от времени всего обращения.
func (m *PlatformMetrics) PlatformQueueWait(class, outcome string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.queues == nil {
		m.queues = make(map[string]int)
	}
	m.queues[class+":"+outcome]++
}

// QueueCount возвращает число ожиданий данного класса и исхода.
func (m *PlatformMetrics) QueueCount(class, outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queues[class+":"+outcome]
}
