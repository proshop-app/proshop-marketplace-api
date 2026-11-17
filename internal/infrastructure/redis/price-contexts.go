package redis

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const priceContextCacheTimeout = 20 * time.Millisecond

var beginPriceContext = goredis.NewScript(`
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', ARGV[3])
for _, key in ipairs(expired) do redis.call('DEL', key) end
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', ARGV[3])
redis.call('HSET', KEYS[1], 'token', ARGV[1])
redis.call('PEXPIRE', KEYS[1], ARGV[2])
redis.call('ZADD', KEYS[2], ARGV[3] + ARGV[2], KEYS[1])
local excess = redis.call('ZCARD', KEYS[2]) - tonumber(ARGV[4])
if excess > 0 then
    local victims = redis.call('ZRANGE', KEYS[2], 0, excess - 1)
    for _, key in ipairs(victims) do
        redis.call('DEL', key)
        redis.call('ZREM', KEYS[2], key)
    end
end
return {redis.call('ZCARD', KEYS[2]), redis.call('HGET', KEYS[1], 'entry') or ''}
`)

var storePriceContext = goredis.NewScript(`
if redis.call('HGET', KEYS[1], 'token') ~= ARGV[1] then return 0 end
redis.call('HSET', KEYS[1], 'entry', ARGV[2])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
redis.call('ZADD', KEYS[2], ARGV[4] + ARGV[3], KEYS[1])
return 1
`)

var deletePriceContext = goredis.NewScript(`
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], KEYS[1])
return redis.call('ZCARD', KEYS[2])
`)

// PriceContexts хранит одну запись на покупателя; индекс ограничивает общий объём без SCAN.
type PriceContexts struct {
	client   *goredis.Client
	prefix   string
	capacity int
	metrics  ports.PriceContextMetrics
}

// NewPriceContexts выделяет пул с короткими сетевыми бюджетами без повторов.
func NewPriceContexts(
	client *goredis.Client,
	prefix string,
	capacity int,
	metrics ports.PriceContextMetrics,
) *PriceContexts {
	options := *client.Options()
	// Default Dialer исходного клиента замыкает его старый DialTimeout; создаём его заново.
	options.Dialer = nil
	options.DialTimeout = priceContextCacheTimeout
	options.ReadTimeout = priceContextCacheTimeout
	options.WriteTimeout = priceContextCacheTimeout
	options.PoolTimeout = priceContextCacheTimeout
	options.ContextTimeoutEnabled = true
	options.MaxRetries = -1
	return &PriceContexts{
		client:   goredis.NewClient(&options),
		prefix:   prefix + "{price-contexts}:",
		capacity: capacity,
		metrics:  metrics,
	}
}

// Get читает временный снимок покупателя.
func (c *PriceContexts) Get(ctx context.Context, buyer ids.UUID) (ports.PriceContextEntry, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, priceContextCacheTimeout)
	defer cancel()
	raw, err := c.client.HGet(ctx, c.key(buyer), "entry").Bytes()
	if errors.Is(err, goredis.Nil) {
		return ports.PriceContextEntry{}, false, nil
	}
	if err != nil {
		return ports.PriceContextEntry{}, false, ErrUnavailable.Wrap(err)
	}
	var entry ports.PriceContextEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return entry, false, ErrUnavailable.Wrap(err)
	}
	return entry, true, nil
}

// Begin атомарно задаёт токен и ограничивает число записей.
func (c *PriceContexts) Begin(
	ctx context.Context,
	buyer ids.UUID,
	token string,
	ttl time.Duration,
) (ports.PriceContextEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, priceContextCacheTimeout)
	defer cancel()
	values, err := beginPriceContext.Run(
		ctx,
		c.client,
		[]string{c.key(buyer), c.prefix + "active"},
		token,
		ttl.Milliseconds(),
		time.Now().UnixMilli(),
		c.capacity,
	).Slice()
	if err != nil {
		return ports.PriceContextEntry{}, ErrUnavailable.Wrap(err)
	}
	return c.beginEntry(values)
}

func (c *PriceContexts) beginEntry(values []any) (ports.PriceContextEntry, error) {
	if len(values) != 2 {
		return ports.PriceContextEntry{}, ErrUnavailable
	}
	size, validSize := values[0].(int64)
	raw, validEntry := values[1].(string)
	if !validSize || !validEntry {
		return ports.PriceContextEntry{}, ErrUnavailable
	}
	c.size(int(size))
	var entry ports.PriceContextEntry
	if raw == "" {
		return entry, nil
	}
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return entry, ErrUnavailable.Wrap(err)
	}
	return entry, nil
}

// Store сохраняет результат только при неизменившемся токене загрузки.
func (c *PriceContexts) Store(
	ctx context.Context,
	buyer ids.UUID,
	token string,
	entry ports.PriceContextEntry,
	ttl time.Duration,
) (bool, error) {
	raw, err := json.Marshal(entry)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, priceContextCacheTimeout)
	defer cancel()
	saved, err := storePriceContext.Run(
		ctx,
		c.client,
		[]string{c.key(buyer), c.prefix + "active"},
		token,
		raw,
		ttl.Milliseconds(),
		time.Now().UnixMilli(),
	).Int()
	if err != nil {
		return false, ErrUnavailable.Wrap(err)
	}
	return saved == 1, nil
}

// Delete удаляет снимок вместе с токеном и записью индекса.
func (c *PriceContexts) Delete(ctx context.Context, buyer ids.UUID) error {
	ctx, cancel := context.WithTimeout(ctx, priceContextCacheTimeout)
	defer cancel()
	size, err := deletePriceContext.Run(ctx, c.client, []string{c.key(buyer), c.prefix + "active"}).Int()
	if err != nil {
		return ErrUnavailable.Wrap(err)
	}
	c.size(size)
	return nil
}

func (c *PriceContexts) key(buyer ids.UUID) string {
	return c.prefix + string(buyer)
}

func (c *PriceContexts) size(size int) {
	if c.metrics != nil {
		c.metrics.PriceContextSize("redis", size)
	}
}

// Close закрывает выделенный пул; основной Redis сессий не затрагивается.
func (c *PriceContexts) Close() error {
	return c.client.Close()
}
