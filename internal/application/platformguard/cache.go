package platformguard

import (
	"container/list"
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
	node    *list.Element
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
	key := reviewKey([]any{tenant, product, limit, offset})
	empty := ports.ReviewPage{Items: []ports.Review{}}
	value := cached(ctx, c, key, empty, func(ctx context.Context) (ports.ReviewPage, error) {
		return c.upstream.ProductReviews(ctx, tenant, product, limit, offset)
	})
	value.Items = append([]ports.Review{}, value.Items...)
	return value, nil
}

func reviewKey(values []any) cacheKey {
	// Здесь только строки, числа и срезы идентификаторов; сериализация не может завершиться ошибкой.
	raw, _ := json.Marshal(values)
	return cacheKey{operation: "reviews", digest: sha256.Sum256(raw)}
}

func cached[T any](ctx context.Context, c *Client, key cacheKey, empty T, load func(context.Context) (T, error)) T {
	started := time.Now()
	c.cacheMu.Lock()
	entry := c.cache[key]
	now := c.clock.Now()
	if entry != nil {
		c.cacheLRU.MoveToBack(entry.node)
		if entry.value != nil && now.Sub(entry.loaded) < c.cfg.CacheTTL {
			value := entry.value.(T)
			c.cacheMu.Unlock()
			return value
		}
		if entry.loading {
			c.cacheMu.Unlock()
			c.observe(ctx, readClass, key.operation, "short_circuit", started)
			return stale(ctx, c, key, entry, empty, started)
		}
	} else {
		c.evict()
		entry = &cacheEntry{node: c.cacheLRU.PushBack(key)}
		c.cache[key] = entry
	}
	entry.loading = true
	c.cacheMu.Unlock()

	value, err := call(ctx, c, readClass, key.operation, c.cfg.ReadTimeout, load)
	c.cacheMu.Lock()
	entry.loading = false
	if err == nil {
		entry.value = value
		entry.loaded = c.clock.Now()
	}
	c.cacheMu.Unlock()
	if err != nil {
		return stale(ctx, c, key, entry, empty, started)
	}
	return value
}

func stale[T any](ctx context.Context, c *Client, key cacheKey, entry *cacheEntry, empty T, started time.Time) T {
	c.cacheMu.Lock()
	value := entry.value
	usable := value != nil && c.clock.Now().Sub(entry.loaded) < c.cfg.CacheTTL+c.cfg.StaleTTL
	c.cacheMu.Unlock()
	if usable {
		c.observe(ctx, readClass, key.operation, "stale", started)
		return value.(T)
	}
	return empty
}

// evict вызывается под cacheMu; вытеснение LRU ограничивает также записи текущих загрузок.
func (c *Client) evict() {
	if len(c.cache) < c.cfg.CacheSize {
		return
	}
	oldest := c.cacheLRU.Front()
	delete(c.cache, oldest.Value.(cacheKey))
	c.cacheLRU.Remove(oldest)
}
