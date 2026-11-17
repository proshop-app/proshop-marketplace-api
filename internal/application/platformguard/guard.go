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
	ReadTimeout      time.Duration
	StrictTimeout    time.Duration
	CacheTTL         time.Duration
	StaleTTL         time.Duration
	CacheSize        int
	MaxConcurrent    int
	TenantStateSize  int
	TenantIdleTTL    time.Duration
	FailureThreshold int
	OpenInterval     time.Duration
	LogInterval      time.Duration
}

// Client оборачивает порт, поэтому защита проверяется без HTTP и настоящего сервера.
type Client struct {
	upstream ports.PlatformAPI
	cfg      Config
	clock    ports.Clock
	metrics  ports.PlatformMetrics
	logger   *slog.Logger
	slots    chan struct{}
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
		tenants:  make(map[ids.TenantID]*tenantState),
		idle:     list.New(),
		logs:     make(map[string]time.Time),
		cache:    make(map[cacheKey]*cacheEntry),
	}
}

func (c *Client) acquire(ctx context.Context) (permit, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.clock.Now()
	if !c.server.available(now) {
		return permit{}, false
	}
	tenant, _ := ctx.Value(tenantContextKey{}).(ids.TenantID)
	state := c.tenantState(tenant, now)
	if tenant != "" && (state == nil || !state.breaker.available(now)) {
		return permit{}, false
	}
	select {
	case c.slots <- struct{}{}:
	default:
		return permit{}, false
	}
	p := permit{server: c.server.start(), tenant: state}
	if state != nil {
		c.pin(state)
		state.active++
		p.shop = state.breaker.start()
	}
	return p, true
}

func (c *Client) release(p permit, err error) {
	defer func() { <-c.slots }()
	c.mu.Lock()
	defer c.mu.Unlock()

	var serverFailure *ports.PlatformServerFailure
	shared := errors.As(err, &serverFailure)
	canceled := errors.Is(err, context.Canceled)
	now := c.clock.Now()
	c.server.finish(p.server, shared, canceled, c.cfg, now)
	if p.tenant != nil {
		p.tenant.breaker.finish(p.shop, tenantFailure(err), shared || canceled, c.cfg, now)
		p.tenant.active--
		if p.tenant.active == 0 {
			c.unpin(p.tenant)
		}
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
	p, allowed := c.acquire(ctx)
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
