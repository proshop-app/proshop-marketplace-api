// Package platformguard ограничивает влияние сервера магазинов на обработчики площадки.
package platformguard

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

var (
	// ErrTimeout — строгая операция не завершилась за отведённое время.
	ErrTimeout = errs.New(errs.Unavailable, "platform.timeout", "магазин не ответил вовремя, попробуйте позже")
	// ErrUnavailable — ячейка временно исключена или исчерпан лимит обращений.
	ErrUnavailable = errs.New(errs.Unavailable, "platform.unavailable", "магазин временно недоступен, попробуйте позже")
)

// Config приходит из точки сборки; окружение проверяется до создания клиента.
type Config struct {
	ReadTimeout      time.Duration
	StrictTimeout    time.Duration
	CacheTTL         time.Duration
	StaleTTL         time.Duration
	CacheSize        int
	MaxConcurrent    int
	FailureThreshold int
	OpenInterval     time.Duration
	LogInterval      time.Duration
}

// Client оборачивает порт, поэтому защита проверяется без HTTP и настоящего сервера.
type Client struct {
	upstream   ports.PlatformAPI
	cfg        Config
	clock      ports.Clock
	metrics    ports.PlatformMetrics
	logger     *slog.Logger
	slots      chan struct{}
	mu         sync.Mutex
	failures   int
	generation uint64
	openUntil  time.Time
	probe      bool
	logs       map[string]time.Time
	cache      map[cacheKey]*cacheEntry
	sequence   uint64
}

type tenantContextKey struct{}

type permit struct {
	generation uint64
	probe      bool
}

// New создаёт общие для процесса ограничитель, предохранитель и ограниченный кэш.
func New(
	upstream ports.PlatformAPI,
	cfg Config,
	clock ports.Clock,
	metrics ports.PlatformMetrics,
	logger *slog.Logger,
) *Client {
	return &Client{
		upstream: upstream,
		cfg:      cfg,
		clock:    clock,
		metrics:  metrics,
		logger:   logger,
		slots:    make(chan struct{}, cfg.MaxConcurrent),
		logs:     make(map[string]time.Time),
		cache:    make(map[cacheKey]*cacheEntry),
	}
}

func (c *Client) acquire() (permit, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	if !c.openUntil.IsZero() && (now.Before(c.openUntil) || c.probe) {
		return permit{}, false
	}
	select {
	case c.slots <- struct{}{}:
	default:
		return permit{}, false
	}
	p := permit{generation: c.generation, probe: !c.openUntil.IsZero()}
	if p.probe {
		c.probe = true
	}
	return p, true
}

func (c *Client) release(p permit, err error) {
	defer func() { <-c.slots }()
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.generation != c.generation {
		return
	}
	if errors.Is(err, context.Canceled) {
		c.probe = false
		return
	}
	// Отказ бизнес-правила подтверждает доступность сервера и не открывает предохранитель.
	failed := err != nil && (errs.CodeOf(err) == errs.Unavailable || errs.CodeOf(err) == errs.Internal)
	var refusal *ports.OrderRefusal
	if errors.As(err, &refusal) && !refusal.Unexpected && refusal.Code != "MARKETPLACE_TEMPORARY_FAILURE" {
		failed = false
	}
	if errors.Is(err, ports.ErrMarketplaceDisabled) {
		failed = false
	}
	if !failed {
		c.failures = 0
		c.openUntil = time.Time{}
		c.probe = false
		if p.probe {
			c.generation++
		}
		return
	}
	c.failures++
	if p.probe || c.failures >= c.cfg.FailureThreshold {
		c.openUntil = c.clock.Now().Add(c.cfg.OpenInterval)
		c.probe = false
		c.generation++
	}
}

func call[T any](
	ctx context.Context,
	c *Client,
	operation string,
	timeout time.Duration,
	load func(context.Context) (T, error),
) (T, error) {
	started := time.Now()
	p, allowed := c.acquire()
	if !allowed {
		c.observe(ctx, operation, "short_circuit", started)
		var zero T
		return zero, ErrUnavailable
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	value, err := load(callCtx)
	if callCtx.Err() != nil {
		err = callCtx.Err()
	}
	c.release(p, err)
	outcome := "ok"
	if err != nil {
		outcome = "error"
		if errors.Is(err, context.DeadlineExceeded) {
			outcome = "timeout"
			err = ErrTimeout.Wrap(err)
		} else if errs.CodeOf(err) == errs.Internal {
			err = ErrUnavailable.Wrap(err)
		}
	}
	c.observe(ctx, operation, outcome, started)
	return value, err
}

func (c *Client) observe(ctx context.Context, operation, outcome string, started time.Time) {
	if c.metrics != nil {
		c.metrics.PlatformCall(operation, outcome, time.Since(started))
	}
	if outcome == "ok" || outcome == "stale" || c.logger == nil {
		return
	}
	// Набор причин фиксирован: идентификаторы покупателей и текст ошибки не раздувают память и метрики.
	key := operation + ":" + outcome
	now := c.clock.Now()
	c.mu.Lock()
	last, found := c.logs[key]
	log := !found || now.Sub(last) >= c.cfg.LogInterval
	if log {
		c.logs[key] = now
	}
	c.mu.Unlock()
	if log {
		c.logger.WarnContext(
			ctx,
			"обращение к серверу магазинов не завершено",
			slog.String("operation", operation),
			slog.String("reason", outcome),
			slog.Any("tenantId", ctx.Value(tenantContextKey{})),
		)
	}
}
