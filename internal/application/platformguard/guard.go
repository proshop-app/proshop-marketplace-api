// Package platformguard ограничивает влияние сервера магазинов на обработчики площадки.
package platformguard

import (
	"container/list"
	"log/slog"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

var (
	// ErrTimeout — строгая операция не завершилась за отведённое время.
	ErrTimeout = ports.ErrPlatformTimeout
	// ErrUnavailable — размыкатель открыт или очередь заполнена.
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
	QueueSize             int
	TenantQueueSize       int
	TenantStateSize       int
	TenantIdleTTL         time.Duration
	FailureThreshold      int
	FailureWindow         time.Duration
	FailureMinCalls       int
	FailurePercent        int
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
	running  int
	pools    [classCount]slotPool
	batch    *tenantState
	mu       sync.Mutex
	server   breaker
	tenants  map[ids.TenantID]*tenantState
	idle     *list.List
	logs     map[string]time.Time
	cacheMu  sync.Mutex
	cache    map[cacheKey]*cacheEntry
	cacheLRU list.List
}

type tenantContextKey struct{}

type permit struct {
	server breakerPermit
	shop   breakerPermit
	tenant *tenantState
	class  operationClass
	// strict — вызов шёл со строгим бюджетом или бюджетом вызывающего, а не коротким бюджетом чтения:
	// только такое истечение срока доказывает зависание общему серверу.
	strict bool
}

// New создаёт защиту процесса с изоляцией магазинов и классов операций.
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
		batch:    &tenantState{},
		tenants:  make(map[ids.TenantID]*tenantState),
		idle:     list.New(),
		logs:     make(map[string]time.Time),
		cache:    make(map[cacheKey]*cacheEntry),
	}
}
