// Package platformguard ограничивает влияние сервера магазинов на обработчики площадки.
package platformguard

import (
	"container/list"
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

var (
	// ErrTimeout — строгая операция не завершилась за отведённое время.
	ErrTimeout = errs.New(errs.Unavailable, "platform.timeout", "магазин не ответил вовремя, попробуйте позже")
	// ErrUnavailable — ячейка временно исключена или исчерпан лимит обращений.
	ErrUnavailable = errs.New(errs.Unavailable, "platform.unavailable", "магазин временно недоступен, попробуйте позже")
)

// Config приходит из точки сборки; окружение проверяется до создания клиента.
type Config struct {
	ReadTimeout           time.Duration
	StrictTimeout         time.Duration
	CacheTTL              time.Duration
	StaleTTL              time.Duration
	CacheSize             int
	MaxConcurrent         int
	TenantMaxConcurrent   int
	CheckoutMaxConcurrent int
	ReadMaxConcurrent     int
	TenantStateSize       int
	TenantIdleTTL         time.Duration
	FailureThreshold      int
	OpenInterval          time.Duration
	LogInterval           time.Duration
}

// Client оборачивает порт, поэтому защита проверяется без HTTP и настоящего сервера.
type Client struct {
	upstream ports.PlatformAPI
	cfg      Config
	clock    ports.Clock
	metrics  ports.PlatformMetrics
	logger   *slog.Logger
	slots    chan struct{}
	active   [classCount]int
	batch    *tenantState
	mu       sync.Mutex
	server   breaker
	tenants  map[ids.TenantID]*tenantState
	idle     *list.List
	logs     map[string]time.Time
	cache    map[cacheKey]*cacheEntry
	sequence uint64
}

type tenantContextKey struct{}

type permit struct {
	server breakerPermit
	shop   breakerPermit
	tenant *tenantState
	class  operationClass
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
		batch:    &tenantState{},
		tenants:  make(map[ids.TenantID]*tenantState),
		idle:     list.New(),
		logs:     make(map[string]time.Time),
		cache:    make(map[cacheKey]*cacheEntry),
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
	p, allowed := c.acquire(ctx, classOf(operation))
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
