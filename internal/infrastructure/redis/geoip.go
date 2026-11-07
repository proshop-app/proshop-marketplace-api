package redis

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/ipnet"
)

const (
	geoIPTTL        = 24 * time.Hour
	geoIPFailureTTL = time.Minute
)

// GeoIP — кэширующая обёртка провайдера с общей паузой при сбое.
type GeoIP struct {
	client *goredis.Client
	source ports.GeoIP
	secret string
	prefix string
}

// NewGeoIP — оборачивает провайдер суточным кэшем существующего Redis-клиента.
func NewGeoIP(client *goredis.Client, source ports.GeoIP, prefix, secret string) *GeoIP {
	return &GeoIP{client: client, source: source, prefix: prefix + "geoip:", secret: secret}
}

// Locate — возвращает регион или nil при отсутствии подсказки.
func (g *GeoIP) Locate(ctx context.Context, ip string) (*buyer.Region, error) {
	if region, hit := g.Cached(ctx, ip); hit {
		return region, nil
	}
	return g.LocateUncached(ctx, ip)
}

// LocateUncached обращается к провайдеру после уже проверенного промаха кэша.
func (g *GeoIP) LocateUncached(ctx context.Context, ip string) (*buyer.Region, error) {
	address := net.ParseIP(ip)
	if address == nil {
		return nil, nil
	}
	region, err := g.source.Locate(ctx, address.String())
	if err != nil {
		if ctx.Err() == nil {
			_ = g.client.Set(ctx, g.prefix+"down", "1", geoIPFailureTTL).Err()
		}
		return nil, err
	}
	if g.secret != "" {
		raw, err := json.Marshal(region)
		if err != nil {
			return nil, err
		}
		_ = g.client.Set(ctx, g.key(ip), raw, geoIPTTL).Err()
	}
	return region, nil
}

// Cached читает кэш подсказок и общий признак отказа без обращения к провайдеру.
func (g *GeoIP) Cached(ctx context.Context, ip string) (*buyer.Region, bool) {
	if net.ParseIP(ip) == nil {
		return nil, true
	}
	if g.secret != "" {
		raw, err := g.client.Get(ctx, g.key(ip)).Bytes()
		var region *buyer.Region
		if err == nil && json.Unmarshal(raw, &region) == nil {
			return region, true
		}
	}
	if down, err := g.client.Exists(ctx, g.prefix+"down").Result(); err == nil && down > 0 {
		return nil, true
	}
	return nil, false
}

func (g *GeoIP) key(ip string) string {
	hash := hmac.New(sha256.New, []byte(g.secret))
	_, _ = hash.Write([]byte(ipnet.Network(ip)))
	return g.prefix + hex.EncodeToString(hash.Sum(nil))
}
