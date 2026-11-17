// Package pricecontexts отделяет показ цен от доступности сервера магазинов.
package pricecontexts

import (
	"context"
	"errors"
	"sync"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Config задаёт свежесть записи и предел фоновой работы для всего процесса.
type Config struct {
	TTL           time.Duration
	StaleTTL      time.Duration
	LoadTimeout   time.Duration
	MaxConcurrent int
}

// Service не вызывает сервер магазинов в PriceContexts и Preload.
type Service struct {
	cancel   context.CancelFunc
	workers  sync.WaitGroup
	stopped  bool
	ctx      context.Context
	cfg      Config
	uow      ports.UnitOfWork
	upstream ports.CatalogPricing
	cache    ports.PriceContextCache
	clock    ports.Clock
	ids      ports.IDs
	metrics  ports.PriceContextMetrics
	mu       sync.Mutex
	loading  map[ids.UUID]bool
}

// DisplayPlatform сохраняет остальные операции API, заменяя только контексты показа.
type DisplayPlatform struct {
	ports.PlatformAPI
	contexts *Service
}

// New создаёт общий для процесса загрузчик с отменой при остановке.
func New(
	ctx context.Context,
	cfg Config,
	uow ports.UnitOfWork,
	upstream ports.CatalogPricing,
	cache ports.PriceContextCache,
	clock ports.Clock,
	identities ports.IDs,
	metrics ports.PriceContextMetrics,
) *Service {
	ctx, cancel := context.WithCancel(ctx)
	return &Service{
		cancel:   cancel,
		ctx:      ctx,
		cfg:      cfg,
		uow:      uow,
		upstream: upstream,
		cache:    cache,
		clock:    clock,
		ids:      identities,
		metrics:  metrics,
		loading:  make(map[ids.UUID]bool),
	}
}

// ForDisplay используется только каталогом и панелью; обязательные расчёты получают строгий API.
func (s *Service) ForDisplay(platform ports.PlatformAPI) *DisplayPlatform {
	return &DisplayPlatform{PlatformAPI: platform, contexts: s}
}

// PriceContexts читает только кэш показа, остальные методы сохраняет строгими.
func (p *DisplayPlatform) PriceContexts(
	ctx context.Context,
	buyer ids.UUID,
	tenants []ids.TenantID,
) (map[ids.TenantID]string, error) {
	return p.contexts.PriceContexts(ctx, buyer, tenants)
}

// Preload позволяет повторить загрузку, если после чтения снимка появился новый магазин.
func (p *DisplayPlatform) Preload(buyer ids.UUID) {
	p.contexts.Preload(buyer)
}

// PriceContexts сразу выбирает публичные профили при отсутствии подтверждённого контекста.
func (s *Service) PriceContexts(
	ctx context.Context,
	buyer ids.UUID,
	tenants []ids.TenantID,
) (map[ids.TenantID]string, error) {
	if buyer == "" {
		return nil, nil
	}
	entry, found, err := s.cache.Get(ctx, buyer)
	age := s.clock.Now().Sub(entry.LoadedAt)
	usable := err == nil && found && age >= 0 && age < s.cfg.TTL+s.cfg.StaleTTL
	outcome := "miss"
	switch {
	case err != nil:
		outcome = "error"
	case found && age >= s.cfg.TTL:
		outcome = "stale"
	case usable:
		outcome = "hit"
	}
	result := make(map[ids.TenantID]string, len(tenants))
	if tenants == nil && usable {
		for tenant, profile := range entry.Profiles {
			result[tenant] = profile
		}
	}
	complete := usable
	for _, tenant := range tenants {
		result[tenant] = catalog.DefaultPriceProfile
		if usable && entry.Profiles[tenant] != "" {
			result[tenant] = entry.Profiles[tenant]
		} else {
			complete = false
		}
	}
	s.observe("cache", outcome, 0)
	if !complete || age >= s.cfg.TTL {
		s.Preload(buyer)
	}
	if !complete {
		// Один ответ использует один режим: частичный снимок не выдаёт смесь старых личных цен.
		for tenant := range result {
			result[tenant] = catalog.DefaultPriceProfile
		}
	}
	mode := "public"
	for _, profile := range result {
		if profile != catalog.DefaultPriceProfile {
			mode = "personal"
			break
		}
	}
	s.observe("response", mode, 0)
	return result, nil
}

// Preload не создаёт очередь или горутину на каждую страницу: занятый лимит означает пропуск.
func (s *Service) Preload(buyer ids.UUID) {
	if buyer == "" || s.ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	if s.stopped || s.loading[buyer] || len(s.loading) >= s.cfg.MaxConcurrent {
		s.mu.Unlock()
		s.observe("load", "skipped", 0)
		return
	}
	s.loading[buyer] = true
	s.workers.Add(1)
	s.mu.Unlock()
	go s.load(buyer)
}

func (s *Service) load(buyer ids.UUID) {
	defer s.workers.Done()
	started := time.Now()
	outcome := "error"
	defer func() {
		s.mu.Lock()
		delete(s.loading, buyer)
		s.mu.Unlock()
		s.observe("load", outcome, time.Since(started))
	}()
	ctx, cancel := context.WithTimeout(s.ctx, s.cfg.LoadTimeout)
	defer cancel()
	token := string(s.ids.NewUUID())
	lifetime := s.cfg.TTL + s.cfg.StaleTTL
	if err := s.cache.Begin(ctx, buyer, token, lifetime+s.cfg.LoadTimeout); err != nil {
		outcome = "cache_error"
		return
	}
	// Полный снимок покупателя одинаков для каталога, карточки, рекомендаций и панели.
	loadedAt := s.clock.Now()
	profiles, err := appcatalog.LoadPriceContexts(ctx, s.uow, s.upstream, buyer)
	if err == nil {
		var saved bool
		saved, err = s.cache.Store(
			ctx,
			buyer,
			token,
			ports.PriceContextEntry{
				Profiles: profiles,
				LoadedAt: loadedAt,
			},
			lifetime,
		)
		if err == nil && !saved {
			outcome = "invalidated"
			return
		}
	}
	outcome = "ok"
	if err != nil {
		outcome = "error"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			outcome = "timeout"
		}
	}
}

func (s *Service) observe(operation, outcome string, elapsed time.Duration) {
	if s.metrics != nil {
		s.metrics.PriceContext(operation, outcome, elapsed)
	}
}

// Stop отменяет загрузки и дожидается их завершения до закрытия портов процесса.
func (s *Service) Stop() {
	s.mu.Lock()
	s.stopped = true
	s.cancel()
	s.mu.Unlock()
	s.workers.Wait()
}
