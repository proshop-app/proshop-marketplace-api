package redis_test

import (
	"context"
	"errors"
	"net"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

type geoCacheHook struct {
	values map[string]string
	reads  int
}

func (h *geoCacheHook) DialHook(next goredis.DialHook) goredis.DialHook { return next }
func (h *geoCacheHook) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return next
}
func (h *geoCacheHook) ProcessHook(_ goredis.ProcessHook) goredis.ProcessHook {
	return func(_ context.Context, cmd goredis.Cmder) error {
		key := cmd.Args()[1].(string)
		switch c := cmd.(type) {
		case *goredis.IntCmd:
			if _, ok := h.values[key]; ok {
				c.SetVal(1)
			}
		case *goredis.StringCmd:
			h.reads++
			value, ok := h.values[key]
			if !ok {
				c.SetErr(goredis.Nil)
				return goredis.Nil
			}
			c.SetVal(value)
		case *goredis.StatusCmd:
			switch value := cmd.Args()[2].(type) {
			case string:
				h.values[key] = value
			case []byte:
				h.values[key] = string(value)
			}
			c.SetVal("OK")
		}
		return nil
	}
}

func TestGeoIPSharedFailureAndCancellation(t *testing.T) {
	for _, secret := range []string{"", "01234567890123456789012345678901"} {
		t.Run(secret, func(t *testing.T) {
			client := goredis.NewClient(&goredis.Options{
				Dialer: func(context.Context, string, string) (net.Conn, error) {
					return nil, errors.New("сеть запрещена")
				},
			})
			t.Cleanup(func() { _ = client.Close() })
			hook := &geoCacheHook{values: map[string]string{}}
			client.AddHook(hook)
			source := &fake.GeoIP{Region: &buyer.Region{Code: "RU-AD"}}
			cache := redis.NewGeoIP(client, source, "test:", secret)
			ctx := context.Background()
			for i := 0; i < 2; i++ {
				if region, err := cache.Locate(ctx, "192.0.2.1"); err != nil || region == nil {
					t.Fatal(region, err)
				}
			}
			expected := 1
			if secret == "" {
				expected = 2
			}
			if source.Calls != expected {
				t.Fatal(source.Calls)
			}
			hook.values = map[string]string{}
			source.Err = errors.New("провайдер недоступен")
			if _, err := cache.Locate(ctx, "192.0.2.1"); err == nil {
				t.Fatal("нет ошибки")
			}
			if _, err := cache.Locate(ctx, "198.51.100.1"); err != nil {
				t.Fatal(err)
			}
			if source.Calls != expected+1 || hook.values["test:geoip:down"] != "1" {
				t.Fatal(source.Calls, hook.values)
			}
			hook.values = map[string]string{}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			_, _ = cache.Locate(canceled, "192.0.2.1")
			if _, ok := hook.values["test:geoip:down"]; ok {
				t.Fatal("отмена запроса закэширована")
			}
		})
	}
}

func TestRegionReadsCacheOnceOnMiss(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{Addr: "unused"})
	t.Cleanup(func() { _ = client.Close() })
	hook := &geoCacheHook{values: map[string]string{}}
	client.AddHook(hook)
	source := &fake.GeoIP{Region: &buyer.Region{Code: "RU-AD"}}
	cache := redis.NewGeoIP(client, source, "test:", "01234567890123456789012345678901")
	service := appbuyer.NewFeatures(fake.NewStore(), cache, fake.NewPlatform(), fake.NewLimiter())
	region, err := service.Region(context.Background(), "", "192.0.2.1")
	if err != nil || region.Source != appbuyer.RegionIP || hook.reads != 1 || source.Calls != 1 {
		t.Fatal(region, err, hook.reads, source.Calls)
	}
}

func TestGeoIPCachedRegionDuringProviderFailure(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{Addr: "unused"})
	t.Cleanup(func() { _ = client.Close() })
	hook := &geoCacheHook{values: map[string]string{}}
	client.AddHook(hook)
	source := &fake.GeoIP{Region: &buyer.Region{Code: "RU-AD"}}
	cache := redis.NewGeoIP(client, source, "test:", "01234567890123456789012345678901")
	ctx := context.Background()
	if _, err := cache.Locate(ctx, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	hook.values["test:geoip:down"] = "1"
	source.Err = errors.New("провайдер недоступен")
	service := appbuyer.NewFeatures(fake.NewStore(), cache, fake.NewPlatform(), fake.NewLimiter())
	region, err := service.Region(ctx, "", "192.0.2.2")
	if err != nil || region.Source != appbuyer.RegionIP || region.Code == nil || *region.Code != "RU-AD" ||
		source.Calls != 1 {
		t.Fatal(region, err, source.Calls)
	}
}
