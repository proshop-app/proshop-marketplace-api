package redis_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/ipapi"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

func TestCitySourceFailureIsCachedPerIPAndRemainsAnError(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
			},
		),
	)
	defer server.Close()
	client := goredis.NewClient(&goredis.Options{})
	t.Cleanup(func() { _ = client.Close() })
	hook := &geoCacheHook{values: map[string]string{}}
	client.AddHook(hook)
	detector := redis.NewCityDetector(client, ipapi.New(server.URL), "test:")
	for range 20 {
		city, err := detector.DetectCity(context.Background(), "192.0.2.1")
		if !errors.Is(err, ports.ErrCityUnavailable) || city.City != "" {
			t.Fatal("отказ не должен превращаться в успешный пустой ответ", city, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("один IP повторяет отказ источника", calls.Load())
	}
	// Удаление записи представляет истечение короткого TTL; новый запрос разрешён.
	hook.values = map[string]string{}
	_, _ = detector.DetectCity(context.Background(), "192.0.2.1")
	if calls.Load() != 2 {
		t.Fatal("после истечения отрицательного кэша нужен новый запрос", calls.Load())
	}
}

func TestCityRateLimitPausesNewIPsAcrossProcessesAndPreservesWarmCity(t *testing.T) {
	var calls atomic.Int32
	var limited atomic.Bool
	server := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if limited.Load() {
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				_, _ = w.Write([]byte(`{"status":"success","city":"Москва","countryCode":"RU"}`))
			},
		),
	)
	defer server.Close()
	client := goredis.NewClient(&goredis.Options{})
	t.Cleanup(func() { _ = client.Close() })
	hook := &geoCacheHook{values: map[string]string{}}
	client.AddHook(hook)
	first := redis.NewCityDetector(client, ipapi.New(server.URL), "test:")
	second := redis.NewCityDetector(client, ipapi.New(server.URL), "test:")
	ctx := context.Background()
	if _, err := first.DetectCity(ctx, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	limited.Store(true)
	if _, err := first.DetectCity(ctx, "192.0.2.2"); !errors.Is(err, ports.ErrCityRateLimited) {
		t.Fatal("429 должен отличаться от пустого ответа", err)
	}
	for i := range 100 {
		if _, err := second.DetectCity(ctx, fmt.Sprintf("198.51.100.%d", i)); err == nil {
			t.Fatal("общая пауза должна отдавать отказ")
		}
	}
	city, err := second.DetectCity(ctx, "192.0.2.1")
	if err != nil || city.City != "Москва" || calls.Load() != 2 {
		t.Fatal("пауза общая, но не мешает тёплому городу", city, err, calls.Load())
	}
}

func TestCancelledCityRequestDoesNotPoisonCache(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{})
	t.Cleanup(func() { _ = client.Close() })
	hook := &geoCacheHook{values: map[string]string{}}
	client.AddHook(hook)
	server := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"status":"success","city":"Москва","countryCode":"RU"}`))
			},
		),
	)
	defer server.Close()
	detector := redis.NewCityDetector(client, ipapi.New(server.URL), "test:")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = detector.DetectCity(ctx, "192.0.2.1")
	if len(hook.values) != 0 {
		t.Fatal("отмена не является отказом источника", hook.values)
	}
}

func TestCityProviderTimeoutIsCachedWithoutCachingCallerCancellation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		<-request.Context().Done()
	}))
	defer server.Close()
	client := goredis.NewClient(&goredis.Options{})
	t.Cleanup(func() { _ = client.Close() })
	client.AddHook(&geoCacheHook{values: map[string]string{}})
	detector := redis.NewCityDetector(client, ipapi.New(server.URL), "test:")
	for range 3 {
		if _, err := detector.DetectCity(context.Background(), "192.0.2.1"); err == nil {
			t.Fatal("таймаут провайдера должен остаться ошибкой")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("внутренний таймаут клиента должен кэшироваться", calls.Load())
	}
}
