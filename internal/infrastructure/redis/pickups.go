package redis

import (
	"context"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const pickupCacheTimeout = 100 * time.Millisecond

// PickupCache отделён от ключей сессий и хранит только неперсональные данные.
type PickupCache struct {
	client *goredis.Client
	prefix string
}

// NewPickupCache изолирует ключи публичных списков ПВЗ от других данных Redis.
func NewPickupCache(client *goredis.Client, prefix string) *PickupCache {
	return &PickupCache{client: client.WithTimeout(pickupCacheTimeout), prefix: prefix + "pickup-points:"}
}

// Get отличает промах от отказа, чтобы вызывающий код мог обратиться к магазину.
func (c *PickupCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	// Ограничиваем также ожидание пула и dial; WithTimeout ограничивает сетевое чтение/запись.
	ctx, cancel := context.WithTimeout(ctx, pickupCacheTimeout)
	defer cancel()
	value, err := c.client.Get(ctx, c.prefix+key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, false, nil
	}
	return value, err == nil, err
}

// Set ограничивает срок жизни любого варианта публичного списка.
func (c *PickupCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, pickupCacheTimeout)
	defer cancel()
	return c.client.Set(ctx, c.prefix+key, value, ttl).Err()
}
