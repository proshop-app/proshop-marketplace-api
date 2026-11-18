package redis_test

import (
	"context"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

func TestCityDetectionCacheKeepsSuccessfulAndEmptyResponses(t *testing.T) {
	for _, city := range []string{"Город", ""} {
		t.Run(city, func(t *testing.T) {
			client := goredis.NewClient(&goredis.Options{})
			t.Cleanup(func() { _ = client.Close() })
			client.AddHook(&geoCacheHook{values: map[string]string{}})
			source := &fake.Cities{Detection: ports.DetectedCity{City: city, Country: "RU"}}
			detector := redis.NewCityDetector(client, source, "test:")
			for range 2 {
				detected, err := detector.DetectCity(context.Background(), "192.0.2.1")
				if err != nil || detected.City != city {
					t.Fatal(detected, err)
				}
			}
			if len(source.IPs) != 1 {
				t.Fatal("повтор одного IP должен попадать в кэш", source.IPs)
			}
		})
	}
}
