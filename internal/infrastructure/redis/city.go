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

// Сроки из proshop-base/constants/geo.ts: результат — сутки, пустой — пять минут.
const cityCacheTTL = 24 * time.Hour
const cityEmptyTTL = 5 * time.Minute

// CityDetector кэширует ответ IP-источника по срокам proshop-base.
type CityDetector struct {
	failed atomic.Bool
	client *goredis.Client
	source ports.CityDetector
	prefix string
}

// NewCityDetector оборачивает источник общим кэшем Redis.
func NewCityDetector(client *goredis.Client, source ports.CityDetector, prefix string) *CityDetector {
	return &CityDetector{client: client, source: source, prefix: prefix + "city-ip-v1:"}
}

// DetectCity не кэширует сбой источника как пустой город.
func (d *CityDetector) DetectCity(ctx context.Context, ip string) (ports.DetectedCity, error) {
	digest := sha256.Sum256([]byte(ip))
	key := d.prefix + hex.EncodeToString(digest[:])
	var city ports.DetectedCity
	raw, err := d.client.Get(ctx, key).Bytes()
	d.observe(err)
	if err == nil && json.Unmarshal(raw, &city) == nil {
		return city, nil
	}
	city, err = d.source.DetectCity(ctx, ip)
	if err != nil {
		return city, err
	}
	ttl := cityCacheTTL
	if city.City == "" {
		ttl = cityEmptyTTL
	}
	raw, err = json.Marshal(city)
	if err == nil {
		d.observe(d.client.Set(ctx, key, raw, ttl).Err())
	}
	return city, nil
}

func (d *CityDetector) observe(err error) {
	if err == nil || errors.Is(err, goredis.Nil) {
		d.failed.Store(false)
	} else if d.failed.CompareAndSwap(false, true) {
		slog.Warn("Redis: кэш определения города недоступен; используется источник")
	}
}
