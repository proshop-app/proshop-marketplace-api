package contextcache

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Fallback использует память только при недоступности Redis; промах Redis не оживляет старую локальную запись.
type Fallback struct {
	primary ports.PriceContextCache
	memory  ports.PriceContextCache
}

// NewFallback подключает резерв; nil primary означает работу только в памяти.
func NewFallback(primary, memory ports.PriceContextCache) *Fallback {
	return &Fallback{primary: primary, memory: memory}
}

// Get не использует локальный снимок после подтверждённого общего промаха.
func (c *Fallback) Get(ctx context.Context, buyer ids.UUID) (ports.PriceContextEntry, bool, error) {
	if c.primary != nil {
		entry, found, err := c.primary.Get(ctx, buyer)
		if err == nil {
			if !found {
				_ = c.memory.Delete(ctx, buyer)
			}
			return entry, found, nil
		}
	}
	return c.memory.Get(ctx, buyer)
}

// Begin допускает загрузку при отказе общего кэша.
func (c *Fallback) Begin(
	ctx context.Context,
	buyer ids.UUID,
	token string,
	ttl time.Duration,
) (ports.PriceContextEntry, error) {
	if c.primary != nil {
		entry, err := c.primary.Begin(ctx, buyer, token, ttl)
		if err == nil {
			if entry.LoadedAt.IsZero() {
				_ = c.memory.Delete(ctx, buyer)
			}
			_, err = c.memory.Begin(ctx, buyer, token, ttl)
			return entry, err
		}
	}
	// Память продолжает обслуживать процесс при отказе общего кэша.
	return c.memory.Begin(ctx, buyer, token, ttl)
}

// Store проверяет токен в обоих хранилищах и не возрождает сброшенную запись.
func (c *Fallback) Store(
	ctx context.Context,
	buyer ids.UUID,
	token string,
	entry ports.PriceContextEntry,
	ttl time.Duration,
) (bool, error) {
	saved, err := c.memory.Store(ctx, buyer, token, entry, ttl)
	if c.primary != nil {
		shared, sharedErr := c.primary.Store(ctx, buyer, token, entry, ttl)
		if sharedErr == nil {
			if !shared {
				_ = c.memory.Delete(ctx, buyer)
			}
			return shared, nil
		}
	}
	return saved, err
}

// Delete сбрасывает локальное и общее значение; общий отказ требует повтора события.
func (c *Fallback) Delete(ctx context.Context, buyer ids.UUID) error {
	if err := c.memory.Delete(ctx, buyer); err != nil {
		return err
	}
	if c.primary != nil {
		// Inbox откатится при отказе Redis: повтор доставки завершит общий сброс.
		return c.primary.Delete(ctx, buyer)
	}
	return nil
}
