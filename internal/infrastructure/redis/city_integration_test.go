//go:build integration

package redis_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

func TestCityFailureAndSharedPauseHaveBoundedTTLs(t *testing.T) {
	ctx := context.Background()
	address := os.Getenv("TEST_REDIS_URL")
	if address == "" {
		address = "redis://localhost:26379/0"
	}
	client, err := redis.Open(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	p := prefix()
	ip := "192.0.2.1"
	digest := sha256.Sum256([]byte(ip))
	key := p + "city-ip-v2:" + hex.EncodeToString(digest[:])
	pauseKey := p + "city-ip-v2:paused"
	defer client.Del(ctx, key, pauseKey)
	source := &fake.Cities{Error: ports.ErrCityRateLimited}
	first := redis.NewCityDetector(client, source, p)
	if _, err := first.DetectCity(ctx, ip); err == nil {
		t.Fatal("ожидался отказ источника")
	}
	failureTTL, err := client.PTTL(ctx, key).Result()
	if err != nil || failureTTL <= 0 || failureTTL > 5*time.Second {
		t.Fatal("отказ по IP хранится пять секунд", failureTTL, err)
	}
	pauseTTL, err := client.PTTL(ctx, pauseKey).Result()
	if err != nil || pauseTTL <= 0 || pauseTTL > time.Minute {
		t.Fatal("общая пауза ограничена минутой", pauseTTL, err)
	}
	second := redis.NewCityDetector(client, source, p)
	if _, err := second.DetectCity(ctx, "198.51.100.1"); err == nil || len(source.IPs) != 1 {
		t.Fatal("другой процесс должен соблюдать общую паузу", source.IPs, err)
	}
	if err := client.Del(ctx, key, pauseKey).Err(); err != nil {
		t.Fatal(err)
	}
	source.Error = nil
	source.Detection = ports.DetectedCity{City: "Москва", Country: "RU"}
	recovered, err := second.DetectCity(ctx, ip)
	if err != nil || recovered.City != "Москва" || len(source.IPs) != 2 {
		t.Fatal("после истечения паузы повтор разрешён", recovered, err, source.IPs)
	}
}
