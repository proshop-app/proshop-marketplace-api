// Package redis — сессии покупателей и лимиты частоты в Redis. У каждого
// ключа есть срок жизни: память Redis не растёт без предела.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ErrUnavailable — Redis недоступен.
var ErrUnavailable = errs.New(errs.Unavailable, "redis.unavailable", "хранилище сессий недоступно")

// Sessions — сессии покупателей: ключ — отпечаток токена, срок жизни — до конца сессии.
type Sessions struct {
	client *goredis.Client
	prefix string
}

type sessionJSON struct {
	BuyerID   string    `json:"buyerId"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// fixedWindow — счётчик окна: первая попытка заводит ключ со сроком окна.
var fixedWindow = goredis.NewScript(`
local n = tonumber(ARGV[2])
local count = redis.call('INCRBY', KEYS[1], n)
if count == n then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return {count, redis.call('PTTL', KEYS[1])}
`)

// Limiter — лимиты частоты фиксированным окном.
type Limiter struct {
	client *goredis.Client
	prefix string
}

// NewClient проверяет адрес без подключения: временный отказ Redis не блокирует запуск публичного API.
func NewClient(url string) (*goredis.Client, error) {
	options, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("адрес Redis: %w", err)
	}
	// go-redis по умолчанию заменяет контекст на Background при сетевом I/O.
	// Запрещаем также отключать сроки через URL: у каждой команды есть бюджет.
	if options.ReadTimeout < 0 || options.WriteTimeout < 0 || options.DialTimeout < 0 {
		return nil, fmt.Errorf("сетевые сроки Redis должны быть положительными")
	}
	options.ContextTimeoutEnabled = true
	return goredis.NewClient(options), nil
}

// Open — клиент с проверкой связи; prefix отделяет ключи маркетплейса.
func Open(ctx context.Context, url string) (*goredis.Client, error) {
	client, err := NewClient(url)
	if err != nil {
		return nil, err
	}

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()

		return nil, ErrUnavailable.Wrap(err)
	}

	return client, nil
}

// NewSessions — сессии с префиксом ключей.
func NewSessions(client *goredis.Client, prefix string) *Sessions {
	return &Sessions{client: client, prefix: prefix + "session:"}
}

// Save сохраняет сессию до её конца.
func (s *Sessions) Save(ctx context.Context, session buyer.Session) error {
	doc := sessionJSON{
		BuyerID:   string(session.BuyerID),
		CreatedAt: session.CreatedAt,
		ExpiresAt: session.ExpiresAt,
	}

	encoded, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("сессия: %w", err)
	}

	err = s.client.SetArgs(ctx, s.prefix+session.TokenHash, encoded, goredis.SetArgs{ExpireAt: session.ExpiresAt}).Err()
	if err != nil {
		return ErrUnavailable.Wrap(err)
	}

	return nil
}

// Get — сессия по отпечатку токена.
func (s *Sessions) Get(ctx context.Context, tokenHash string) (buyer.Session, error) {
	raw, err := s.client.Get(ctx, s.prefix+tokenHash).Bytes()
	if errors.Is(err, goredis.Nil) {
		return buyer.Session{}, buyer.ErrSessionNotFound
	}
	if err != nil {
		return buyer.Session{}, ErrUnavailable.Wrap(err)
	}

	var doc sessionJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return buyer.Session{}, fmt.Errorf("сессия: %w", err)
	}

	return buyer.Session{
		TokenHash: tokenHash,
		BuyerID:   ids.UUID(doc.BuyerID),
		CreatedAt: doc.CreatedAt,
		ExpiresAt: doc.ExpiresAt,
	}, nil
}

// Delete удаляет сессию.
func (s *Sessions) Delete(ctx context.Context, tokenHash string) error {
	if err := s.client.Del(ctx, s.prefix+tokenHash).Err(); err != nil {
		return ErrUnavailable.Wrap(err)
	}

	return nil
}

// NewLimiter — лимиты с префиксом ключей.
func NewLimiter(client *goredis.Client, prefix string) *Limiter {
	return &Limiter{client: client, prefix: prefix + "ratelimit:"}
}

// Allow засчитывает попытку атомарно.
func (l *Limiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	return l.AllowN(ctx, key, limit, window, 1)
}

// AllowN расходует несколько попыток за одно обращение к Redis.
func (l *Limiter) AllowN(
	ctx context.Context,
	key string,
	limit int,
	window time.Duration,
	n int,
) (bool, time.Duration, error) {
	result, err := fixedWindow.Run(ctx, l.client, []string{l.prefix + key}, window.Milliseconds(), n).Int64Slice()
	if err != nil {
		return false, 0, ErrUnavailable.Wrap(err)
	}

	count, ttl := result[0], time.Duration(result[1])*time.Millisecond
	if count > int64(limit) {
		return false, ttl, nil
	}

	return true, 0, nil
}
