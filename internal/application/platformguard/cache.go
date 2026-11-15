package platformguard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type cacheKey struct {
	operation string
	digest    [sha256.Size]byte
}

type cacheEntry struct {
	value   any
	loaded  time.Time
	used    uint64
	loading bool
}

// ProductReviews при отказе сохраняет доступность карточки и отдаёт старую или пустую страницу.
func (c *Client) ProductReviews(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
	limit, offset int,
) (ports.ReviewPage, error) {
	ctx = context.WithValue(ctx, tenantContextKey{}, tenant)
	key := newCacheKey("reviews", []any{tenant, product, limit, offset})
	empty := ports.ReviewPage{Items: []ports.Review{}}
	value := cached(ctx, c, key, empty, func(ctx context.Context) (ports.ReviewPage, error) {
		return c.upstream.ProductReviews(ctx, tenant, product, limit, offset)
	})
	value.Items = append([]ports.Review{}, value.Items...)
	return value, nil
}

func newCacheKey(operation string, values []any) cacheKey {
	// Здесь только строки, числа и срезы идентификаторов; сериализация не может завершиться ошибкой.
	raw, _ := json.Marshal(values)
	return cacheKey{operation: operation, digest: sha256.Sum256(raw)}
}

func cached[T any](ctx context.Context, c *Client, key cacheKey, empty T, load func(context.Context) (T, error)) T {
	started := time.Now()
	c.mu.Lock()
	entry := c.cache[key]
	now := c.clock.Now()
	c.sequence++
	if entry != nil {
		entry.used = c.sequence
		if entry.value != nil && now.Sub(entry.loaded) < c.cfg.CacheTTL {
			value := entry.value.(T)
			c.mu.Unlock()
			return value
		}
		if entry.loading {
			c.mu.Unlock()
			c.observe(ctx, key.operation, "short_circuit", started)
			return stale(ctx, c, key, entry, empty, started)
		}
	} else {
		c.evict()
		entry = &cacheEntry{used: c.sequence}
		c.cache[key] = entry
	}
	entry.loading = true
	c.mu.Unlock()

	value, err := call(ctx, c, key.operation, c.cfg.ReadTimeout, load)
	c.mu.Lock()
	entry.loading = false
	if err == nil {
		entry.value = value
		entry.loaded = c.clock.Now()
	}
	c.mu.Unlock()
	if err != nil {
		return stale(ctx, c, key, entry, empty, started)
	}
	return value
}

func stale[T any](ctx context.Context, c *Client, key cacheKey, entry *cacheEntry, empty T, started time.Time) T {
	c.mu.Lock()
	value := entry.value
	usable := value != nil && c.clock.Now().Sub(entry.loaded) < c.cfg.CacheTTL+c.cfg.StaleTTL
	c.mu.Unlock()
	if usable {
		c.observe(ctx, key.operation, "stale", started)
		return value.(T)
	}
	return empty
}

// evict вызывается под mu; вытеснение LRU ограничивает также записи текущих загрузок.
func (c *Client) evict() {
	if len(c.cache) < c.cfg.CacheSize {
		return
	}
	var oldest cacheKey
	var used uint64
	for key, entry := range c.cache {
		if used == 0 || entry.used < used {
			oldest, used = key, entry.used
		}
	}
	delete(c.cache, oldest)
}
