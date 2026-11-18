// Package delivery получает пункты выдачи через магазины.
package delivery

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Бюджет сериализованных справочников, без обрезания большого города: он остаётся один.
const (
	pickupCacheMemoryBytes = 64 * 1024 * 1024
	// Четыре загрузки по 20 с не занимают места оформления; очередь ограничивает фон при переборе городов.
	pickupLoadLimit   = 4
	pickupQueueLimit  = 32
	pickupNegativeTTL = 30 * time.Second
	pickupFailureTTL  = 5 * time.Second
)

// ErrSellerUnreachable — все опрошенные магазины недоступны.
var ErrSellerUnreachable = errs.New(errs.Unavailable, "seller_unreachable", "магазины не ответили")

// Config ограничивает нагрузку и время ожидания магазинов.
type Config struct {
	Attempts     int
	TTL          time.Duration
	QuoteTimeout time.Duration
}

// Service объединяет одновременные промахи одного ключа в пределах процесса.
type Service struct {
	uow        ports.UnitOfWork
	platform   ports.PlatformAPI
	cache      ports.PickupCache
	config     Config
	mu         sync.Mutex
	flights    map[string]*flight
	catalogs   map[string]*pickupCatalog
	cacheBytes int
	loads      chan struct{}
	source     atomic.Uint32
}

type pickupCatalog struct {
	Points    []delivery.PickupPoint `json:"points"`
	Failed    bool                   `json:"failed,omitempty"`
	ExpiresAt time.Time              `json:"expiresAt"`
	index     *delivery.PickupIndex
	bytes     int
	lastUsed  time.Time
}

type flight struct {
	cancel  context.CancelFunc
	done    chan struct{}
	catalog *pickupCatalog
	err     error
}

// NewService собирает поиск пунктов и общий кэш.
func NewService(uow ports.UnitOfWork, platform ports.PlatformAPI, cache ports.PickupCache, cfg Config) *Service {
	return &Service{
		uow:      uow,
		platform: platform,
		cache:    cache,
		config:   cfg,
		flights:  make(map[string]*flight),
		catalogs: make(map[string]*pickupCatalog),
		loads:    make(chan struct{}, pickupLoadLimit),
	}
}

// Carriers — службы, поддерживаемые контрактом настроек магазинов.
func (s *Service) Carriers() []delivery.CarrierInfo {
	return []delivery.CarrierInfo{{Code: "cdek", Name: "СДЭК"}, {Code: "yandex", Name: "Яндекс Доставка"}}
}

// Search возвращает первый успешный список; отказ службы позволяет перейти к следующему магазину.
func (s *Service) Search(ctx context.Context, carrier delivery.Carrier, city string) ([]delivery.PickupPoint, error) {
	catalog, err := s.catalogue(ctx, carrier, city)
	if err != nil {
		return nil, err
	}
	return slices.Clone(catalog.Points), nil
}

func (s *Service) catalogue(
	ctx context.Context,
	carrier delivery.Carrier,
	city string,
) (*pickupCatalog, error) {
	if _, err := delivery.ParseCarrier(string(carrier)); err != nil {
		return nil, err
	}
	city, err := delivery.NormalizeCity(city)
	if err != nil {
		return nil, err
	}
	key := "v3:" + string(carrier) + ":" + strings.ToLower(city)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	now := time.Now()
	for cacheKey, entry := range s.catalogs {
		if !entry.ExpiresAt.After(now) {
			s.cacheBytes -= entry.bytes
			delete(s.catalogs, cacheKey)
		}
	}
	if cached := s.catalogs[key]; cached != nil {
		cached.lastUsed = now
		s.mu.Unlock()
		return cached, catalogueError(cached)
	}
	f, ok := s.flights[key]
	if !ok {
		if len(s.flights) >= pickupLoadLimit+pickupQueueLimit {
			s.mu.Unlock()
			return nil, ErrSellerUnreachable
		}
		// Общая загрузка завершается в своём бюджете, даже если карта уже сменила область.
		budget := time.Duration(s.config.Attempts)*s.config.QuoteTimeout + 5*time.Second
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
		f = &flight{done: make(chan struct{}), cancel: cancel}
		s.flights[key] = f
		go s.load(loadCtx, key, carrier, city, f)
	}
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.done:
		return f.catalog, f.err
	}
}

func (s *Service) load(
	ctx context.Context,
	key string,
	carrier delivery.Carrier,
	city string,
	f *flight,
) {
	defer f.cancel()
	select {
	case s.loads <- struct{}{}:
		defer func() { <-s.loads }()
	case <-ctx.Done():
		f.err = ctx.Err()
	}
	if f.err == nil {
		f.catalog, f.err = s.cached(ctx, key, carrier, city)
	}
	if f.err != nil && f.catalog == nil {
		f.catalog = &pickupCatalog{Failed: true, ExpiresAt: time.Now().Add(pickupFailureTTL)}
		if encoded, err := json.Marshal(f.catalog); err == nil {
			f.catalog.bytes = len(encoded)
			_ = s.cache.Set(ctx, key, encoded, pickupFailureTTL)
		}
	}
	if f.err == nil {
		f.catalog.index = delivery.NewPickupIndex(f.catalog.Points)
	}
	s.mu.Lock()
	if f.catalog != nil {
		s.remember(key, f.catalog)
	}
	if s.flights[key] == f {
		delete(s.flights, key)
	}
	close(f.done)
	s.mu.Unlock()
}

// remember вызывается под mu; индекс и список остаются неизменяемыми для читателей.
func (s *Service) remember(key string, catalog *pickupCatalog) {
	for len(s.catalogs) > 0 && s.cacheBytes+catalog.bytes > pickupCacheMemoryBytes {
		oldestKey := ""
		var oldest time.Time
		for storedKey, value := range s.catalogs {
			if oldest.IsZero() || value.lastUsed.Before(oldest) {
				oldestKey, oldest = storedKey, value.lastUsed
			}
		}
		s.cacheBytes -= s.catalogs[oldestKey].bytes
		delete(s.catalogs, oldestKey)
	}
	catalog.lastUsed = time.Now()
	s.catalogs[key] = catalog
	s.cacheBytes += catalog.bytes
}

func (s *Service) cached(
	ctx context.Context,
	key string,
	carrier delivery.Carrier,
	city string,
) (*pickupCatalog, error) {
	if value, hit, err := s.cache.Get(ctx, key); err == nil && hit {
		var catalog pickupCatalog
		if json.Unmarshal(value, &catalog) == nil && catalog.ExpiresAt.After(time.Now()) {
			catalog.bytes = len(value)
			return &catalog, catalogueError(&catalog)
		}
	}
	candidates, err := s.candidates(ctx, carrier)
	if err != nil {
		return nil, err
	}
	points, err := s.fetch(ctx, carrier, city, candidates)
	if err != nil {
		return nil, err
	}
	ttl := s.config.TTL
	if len(points) == 0 {
		ttl = pickupNegativeTTL
	}
	catalog := &pickupCatalog{Points: points, ExpiresAt: time.Now().Add(ttl)}
	if value, marshalErr := json.Marshal(catalog); marshalErr == nil {
		catalog.bytes = len(value)
		_ = s.cache.Set(ctx, key, value, ttl)
	}
	return catalog, nil
}

// QueryFor фильтрует по уже построенной сетке, не декодируя Redis и не копируя город.
func (s *Service) QueryFor(
	ctx context.Context,
	carrier delivery.Carrier,
	city string,
	regions []string,
	box *delivery.Bounds,
	offset, limit int,
) (delivery.PickupArea, error) {
	catalog, err := s.catalogue(ctx, carrier, city)
	if err != nil {
		return delivery.PickupArea{}, err
	}
	if regions == nil {
		regions = catalog.index.Regions()
	}
	if box != nil {
		return catalog.index.Area(*box, regions), nil
	}
	return catalog.index.List(regions, offset, limit), nil
}

func (s *Service) candidates(
	ctx context.Context,
	carrier delivery.Carrier,
) ([]ids.TenantID, error) {
	var tenants []ids.TenantID
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		keys := []ids.TenantID{}
		after := ids.TenantID("")
		for {
			page, err := tx.Participants().PublicTenants(ctx, nil, after, 200)
			if err != nil {
				return err
			}
			keys = append(keys, page...)
			if len(page) < 200 {
				break
			}
			after = page[len(page)-1]
		}

		participants, err := tx.Participants().Batch(ctx, keys)
		if err != nil {
			return err
		}
		registry, err := tx.Registry().Batch(ctx, keys)
		if err != nil {
			return err
		}
		for _, key := range keys {
			p, tenant := participants[key], registry[key]
			if p != nil && tenant != nil && slices.Contains(p.State().Delivery.Carriers, string(carrier)) &&
				participant.VisibilityOf(p, tenant).CheckoutAvailable {
				tenants = append(tenants, key)
			}
		}
		return nil
	})
	slices.Sort(tenants)
	return tenants, err
}

func (s *Service) fetch(
	ctx context.Context,
	carrier delivery.Carrier,
	city string,
	tenants []ids.TenantID,
) ([]delivery.PickupPoint, error) {
	failed := 0
	attempted := 0
	start := int(s.source.Add(1)-1) % max(1, len(tenants))
	for offset := range len(tenants) {
		tenant := tenants[(start+offset)%len(tenants)]
		if attempted >= s.config.Attempts {
			break
		}
		attempted++
		callCtx, cancel := context.WithTimeout(ctx, s.config.QuoteTimeout)
		callCtx = ports.WithPlatformCallClass(callCtx, ports.PlatformCatalog)
		answer, err := s.platform.PickupPoints(callCtx, tenant, carrier, city)
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			failed++
			continue
		}
		if answer.Available {
			return answer.Items, nil
		}
	}
	if attempted > 0 && failed == attempted {
		return nil, ErrSellerUnreachable
	}
	return []delivery.PickupPoint{}, nil
}

// Point проверяет код по списку выбранной службы в городе.
func (s *Service) Point(
	ctx context.Context,
	carrier delivery.Carrier,
	code, city string,
) (delivery.PickupPoint, error) {
	points, err := s.Search(ctx, carrier, city)
	if err != nil {
		return delivery.PickupPoint{}, err
	}
	for _, point := range points {
		if point.Code == code && point.Carrier == carrier {
			return point, nil
		}
	}
	return delivery.PickupPoint{}, delivery.ErrPickupPointNotFound
}

func catalogueError(catalog *pickupCatalog) error {
	if catalog.Failed {
		return ErrSellerUnreachable
	}
	return nil
}
