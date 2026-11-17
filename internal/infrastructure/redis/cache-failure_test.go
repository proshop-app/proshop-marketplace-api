package redis

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

func TestPublicCachesClassifyConnectionFailures(t *testing.T) {
	cause := errors.New("соединение недоступно")
	client := goredis.NewClient(&goredis.Options{
		MaxRetries: -1,
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			return nil, cause
		},
	})
	defer func() { _ = client.Close() }()
	for _, cache := range []interface {
		Get(context.Context, string) ([]byte, bool, error)
		Set(context.Context, string, []byte, time.Duration) error
	}{NewPickupCache(client, "test:"), NewStoreCardCache(client, "test:")} {
		_, hit, err := cache.Get(context.Background(), "key")
		if hit || errs.CodeOf(err) != errs.Unavailable || !errors.Is(err, cause) {
			t.Fatal(hit, err)
		}
		err = cache.Set(context.Background(), "key", []byte("{}"), time.Second)
		if errs.CodeOf(err) != errs.Unavailable || !errors.Is(err, cause) {
			t.Fatal(err)
		}
	}
}
