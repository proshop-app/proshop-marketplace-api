package redis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

type cityCacheRecord struct {
	City   ports.DetectedCity `json:"city"`
	Failed bool               `json:"failed,omitempty"`
}

// CityDetector кэширует ответ IP-источника по срокам proshop-base.
type CityDetector struct {
	failed      atomic.Bool
	pausedUntil atomic.Int64
	client      *goredis.Client
	source      ports.CityDetector
	prefix      string
}

// NewCityDetector оборачивает источник общим кэшем Redis.
func NewCityDetector(client *goredis.Client, source ports.CityDetector, prefix string) *CityDetector {
	return &CityDetector{client: client, source: source, prefix: prefix + "city-ip-v2:"}
}

// DetectCity отличает пустой город от коротко кэшируемого отказа источника.
func (d *CityDetector) DetectCity(ctx context.Context, ip string) (ports.DetectedCity, error) {
	digest := sha256.Sum256([]byte(ip))
	key := d.prefix + hex.EncodeToString(digest[:])
	raw, err := d.client.Get(ctx, key).Bytes()
	d.observe(err)
	var record cityCacheRecord
	if err == nil && json.Unmarshal(raw, &record) == nil {
		if record.Failed {
			return ports.DetectedCity{}, ports.ErrCityUnavailable
		}
		return record.City, nil
	}
	if d.paused(ctx) {
		return ports.DetectedCity{}, ports.ErrCityUnavailable
	}
	city, err := d.source.DetectCity(ctx, ip)
	if err != nil {
		if ctx.Err() == nil {
			d.cacheCity(ctx, key, cityCacheRecord{Failed: true}, cityFailureTTL)
			if errors.Is(err, ports.ErrCityRateLimited) {
				d.pausedUntil.Store(time.Now().Add(cityRateLimitPause).UnixNano())
				d.observe(d.client.Set(ctx, d.prefix+"paused", "1", cityRateLimitPause).Err())
			}
		}
		return city, err
	}
	ttl := cityCacheTTL
	if city.City == "" {
		ttl = cityEmptyTTL
	}
	d.cacheCity(ctx, key, cityCacheRecord{City: city}, ttl)
	return city, nil
}

func (d *CityDetector) paused(ctx context.Context) bool {
	if time.Now().UnixNano() < d.pausedUntil.Load() {
		return true
	}
	_, err := d.client.Get(ctx, d.prefix+"paused").Result()
	d.observe(err)
	return err == nil
}

func (d *CityDetector) cacheCity(ctx context.Context, key string, record cityCacheRecord, ttl time.Duration) {
	raw, err := json.Marshal(record)
	if err == nil {
		d.observe(d.client.Set(ctx, key, raw, ttl).Err())
	}
}

func (d *CityDetector) observe(err error) {
	if err == nil || errors.Is(err, goredis.Nil) {
		d.failed.Store(false)
	} else if d.failed.CompareAndSwap(false, true) {
		slog.Warn("Redis: кэш определения города недоступен; используется источник")
	}
}
