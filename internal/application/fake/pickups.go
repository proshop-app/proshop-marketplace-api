package fake

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// PickupCall — параметры обращения к магазину.
type PickupCall struct {
	Tenant  ids.TenantID
	Carrier delivery.Carrier
	City    string
}

// PickupCache хранит копии ответов с TTL в памяти.
type PickupCache struct {
	mu      sync.Mutex
	entries map[string]pickupEntry
}

type pickupEntry struct {
	value []byte
	until time.Time
}

// PickupLookup — список магазина для тестов потребителей выбора.
type PickupLookup struct {
	Points []delivery.PickupPoint
}

// Point проверяет код по заданному списку.
func (p *PickupLookup) Point(
	_ context.Context,
	carrier delivery.Carrier,
	code, city string,
) (delivery.PickupPoint, error) {
	if strings.TrimSpace(city) == "" {
		return delivery.PickupPoint{}, delivery.ErrInvalidCity
	}
	for _, point := range p.Points {
		if point.Carrier == carrier && point.Code == code && strings.EqualFold(point.City, city) {
			return point, nil
		}
	}
	return delivery.PickupPoint{}, delivery.ErrPickupPointNotFound
}

// SetPickupPoints задаёт ответ магазина.
func (p *Platform) SetPickupPoints(tenant ids.TenantID, answer ports.PickupPoints) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pickups == nil {
		p.pickups = make(map[ids.TenantID]ports.PickupPoints)
	}
	answer.Items = slices.Clone(answer.Items)
	p.pickups[tenant] = answer
}

// PickupCalls возвращает копию журнала обращений.
func (p *Platform) PickupCalls() []PickupCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.pickupCalls)
}

// PickupPoints — заданный ответ или отказ неподключённой службы.
func (p *Platform) PickupPoints(
	ctx context.Context,
	tenant ids.TenantID,
	carrier delivery.Carrier,
	city string,
) (ports.PickupPoints, error) {
	p.mu.Lock()
	p.pickupCalls = append(p.pickupCalls, PickupCall{Tenant: tenant, Carrier: carrier, City: city})
	answer, ok := p.pickups[tenant]
	err := p.failing[tenant]
	hook := p.BeforePickup
	p.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, tenant); err != nil {
			return ports.PickupPoints{}, err
		}
	}
	if !ok {
		answer.Reason = delivery.ReasonCarrierNotConnected
	}
	answer.Items = slices.Clone(answer.Items)
	return answer, err
}

// NewPickupCache создаёт пустой кэш.
func NewPickupCache() *PickupCache {
	return &PickupCache{entries: make(map[string]pickupEntry)}
}

// Get возвращает копию неистёкшей записи.
func (c *PickupCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	return slices.Clone(entry.value), ok && time.Now().Before(entry.until), nil
}

// Set сохраняет копию ответа до истечения TTL.
func (c *PickupCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = pickupEntry{value: slices.Clone(value), until: time.Now().Add(ttl)}
	return nil
}
