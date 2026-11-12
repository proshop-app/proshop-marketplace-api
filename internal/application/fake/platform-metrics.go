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
}

// PlatformCall потокобезопасно учитывает исход операции.
func (m *PlatformMetrics) PlatformCall(operation, outcome string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.outcomes == nil {
		m.outcomes = make(map[string]int)
	}
	m.outcomes[operation+":"+outcome]++
}

// Count возвращает число наблюдений исхода.
func (m *PlatformMetrics) Count(operation, outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.outcomes[operation+":"+outcome]
}

func (p *Platform) platformCall(ctx context.Context, operation string) error {
	if p.CallHook != nil {
		return p.CallHook(ctx, operation)
	}
	return ctx.Err()
}
