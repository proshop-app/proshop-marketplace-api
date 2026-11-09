package redis

import (
	"context"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const storeCardCacheTimeout = 100 * time.Millisecond

// StoreCardCache отделён от ключей сессий и хранит только неперсональные данные.
type StoreCardCache struct {
	client *goredis.Client
	prefix string
}

// NewStoreCardCache изолирует ключи готовых публичных карточек от других данных Redis.
func NewStoreCardCache(client *goredis.Client, prefix string) *StoreCardCache {
	return &StoreCardCache{client: client.WithTimeout(storeCardCacheTimeout), prefix: prefix + "store-cards:"}
}

// Get отличает промах от отказа, чтобы вызывающий код мог прочитать PostgreSQL.
func (c *StoreCardCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	// Ограничиваем также ожидание пула и dial; WithTimeout ограничивает сетевое чтение/запись.
	ctx, cancel := context.WithTimeout(ctx, storeCardCacheTimeout)
	defer cancel()
	value, err := c.client.Get(ctx, c.prefix+key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, false, nil
	}
	return value, err == nil, err
}

// Set ограничивает срок жизни любого варианта публичного списка.
func (c *StoreCardCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, storeCardCacheTimeout)
	defer cancel()
	return c.client.Set(ctx, c.prefix+key, value, ttl).Err()
}
