// Package delivery получает пункты выдачи через магазины.
package delivery

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/identity"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/safego"
)

// Бюджет сериализованных справочников, без обрезания большого города: он остаётся один.
const (
	pickupCacheMemoryBytes = 64 * 1024 * 1024
	// Четыре загрузки по 20 с не занимают места оформления; очередь ограничивает фон при переборе городов.
	pickupLoadLimit         = 4
	pickupCheckoutLoadLimit = 4
	pickupCleanupBatch      = 256
	pickupCleanupInterval   = time.Second
	pickupQueueLimit        = 32
	pickupNegativeTTL       = 30 * time.Second
	pickupFailureTTL        = 5 * time.Second
)

// ErrSellerUnreachable — источник не ответил, успешного справочника нет.
var ErrSellerUnreachable = errs.New(errs.Unavailable, "seller_unreachable", "магазины не ответили")

// Config ограничивает нагрузку и время ожидания магазинов.
type Config struct {
	Attempts     int
	TTL          time.Duration
	QuoteTimeout time.Duration
}

// Service объединяет одновременные промахи одного ключа в пределах процесса.
type Service struct {
	uow             ports.UnitOfWork
	platform        ports.PlatformAPI
	cache           ports.PickupCache
	config          Config
	mu              sync.Mutex
	flights         map[string]*flight
	selectionOwners map[ids.UUID]*flight
	catalogs        map[string]*list.Element
	lru             list.List
	cleanupCursor   *list.Element
	nextCleanup     time.Time
	checkoutLoads   chan struct{}
	cacheBytes      int
	loads           chan struct{}
	sourceMu        sync.Mutex
	sources         map[delivery.Carrier]ids.TenantID
	cacheReads      singleflight.Group
}

type pickupCatalog struct {
	Points    []delivery.PickupPoint `json:"points"`
	Failed    bool                   `json:"failed,omitempty"`
	ExpiresAt time.Time              `json:"expiresAt"`
	index     *delivery.PickupIndex
	bytes     int
}

type pickupCacheEntry struct {
	key     string
	catalog *pickupCatalog
}

type flight struct {
	owners  []ids.UUID
	cancel  context.CancelFunc
	done    chan struct{}
	catalog *pickupCatalog
	err     error
}

// NewService собирает поиск пунктов и общий кэш.
func NewService(uow ports.UnitOfWork, platform ports.PlatformAPI, cache ports.PickupCache, cfg Config) *Service {
	return &Service{
		uow:             uow,
		platform:        platform,
		cache:           cache,
		config:          cfg,
		flights:         make(map[string]*flight),
		selectionOwners: make(map[ids.UUID]*flight),
		catalogs:        make(map[string]*list.Element),
		loads:           make(chan struct{}, pickupLoadLimit),
		checkoutLoads:   make(chan struct{}, pickupCheckoutLoadLimit),
		sources:         make(map[delivery.Carrier]ids.TenantID),
	}
}

// Carriers — службы, поддерживаемые контрактом настроек магазинов.
func (s *Service) Carriers() []delivery.CarrierInfo {
	return delivery.Carriers()
}

// Search возвращает первый успешный список; отказ службы позволяет перейти к следующему магазину.
func (s *Service) Search(ctx context.Context, carrier delivery.Carrier, city string) ([]delivery.PickupPoint, error) {
	catalog, err := s.catalogue(ctx, carrier, city, "")
	if err != nil {
		return nil, err
	}
	return slices.Clone(catalog.Points), nil
}

func (s *Service) catalogue(
	ctx context.Context,
	carrier delivery.Carrier,
	city string,
	buyerID ids.UUID,
) (*pickupCatalog, error) {
	if !slices.ContainsFunc(s.Carriers(), func(item delivery.CarrierInfo) bool { return item.Code == carrier }) {
		return nil, delivery.ErrInvalidCarrier
	}
	city, err := delivery.NormalizeCity(city)
	if err != nil {
		return nil, err
	}
	class := ports.PlatformCatalog
	if buyerID != "" {
		class = ports.PlatformCheckout
	}
	ctx = ports.WithPlatformCallClass(ctx, class)
	key := "v5:" + string(carrier) + ":" + strings.ToLower(city)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cached := s.memoryCatalog(key); cached != nil {
		return cached, catalogueError(cached)
	}
	// Redis читается до допуска фоновой загрузки: холодный шторм не закрывает тёплый каталог другой реплики.
	if cached := s.sharedCacheRead(ctx, key); cached != nil {
		return cached, catalogueError(cached)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pool := s.loads
	flightKey := key
	if buyerID != "" {
		pool = s.checkoutLoads
		flightKey = "checkout:" + key
	}
	s.mu.Lock()
	// Пока читался Redis, прежняя загрузка могла завершиться и убрать свой flight.
	if cached := s.catalogAt(key, time.Now()); cached != nil {
		s.mu.Unlock()
		return cached, catalogueError(cached)
	}
	f, ok := s.flights[flightKey]
	// Лимит закреплён за загрузкой: уход HTTP-запроса не открывает покупателю следующий холодный город.
	if buyerID != "" {
		if active := s.selectionOwners[buyerID]; active != nil && active != f {
			s.mu.Unlock()
			return nil, delivery.ErrSelectionBusy
		}
	}
	if !ok {
		queued := 0
		for running := range s.flights {
			if strings.HasPrefix(running, "checkout:") == (pool == s.checkoutLoads) {
				queued++
			}
		}
		if queued >= cap(pool)+pickupQueueLimit {
			s.mu.Unlock()
			return nil, ErrSellerUnreachable
		}
		// Общая загрузка завершается в своём бюджете, даже если карта уже сменила область.
		budget := time.Duration(s.config.Attempts)*s.config.QuoteTimeout + 5*time.Second
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
		f = &flight{done: make(chan struct{}), cancel: cancel}
		s.flights[flightKey] = f
		safego.Run(
			"delivery.pickup-load",
			func() error {
				var err error
				f.catalog, err = s.load(loadCtx, key, carrier, city, pool)
				return err
			},
			func(err error) {
				s.finishLoad(loadCtx, key, flightKey, f, err)
			},
		)
	}
	if buyerID != "" && s.selectionOwners[buyerID] == nil {
		s.selectionOwners[buyerID] = f
		f.owners = append(f.owners, buyerID)
	}
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.done:
		return f.catalog, f.err
	}
}

// load занимает место пула загрузок и собирает каталог города с индексом.
func (s *Service) load(
	ctx context.Context,
	key string,
	carrier delivery.Carrier,
	city string,
	pool chan struct{},
) (*pickupCatalog, error) {
	select {
	case pool <- struct{}{}:
		defer func() { <-pool }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	catalog, err := s.fetchCatalog(ctx, key, carrier, city)
	if err == nil {
		catalog.index = delivery.NewPickupIndex(catalog.Points)
	}
	return catalog, err
}

// finishLoad закрывает рейс при любом итоге загрузки, в том числе при панике:
// иначе город навсегда остался бы в рейсе, и все запросы к нему ждали бы до срока.
func (s *Service) finishLoad(ctx context.Context, key, flightKey string, f *flight, err error) {
	defer f.cancel()
	f.err = err
	if errors.Is(f.err, context.DeadlineExceeded) {
		f.err = ErrSellerUnreachable.Wrap(f.err)
	}
	if f.err != nil && f.catalog == nil {
		f.catalog = &pickupCatalog{Failed: true, ExpiresAt: time.Now().Add(pickupFailureTTL)}
		if encoded, err := json.Marshal(f.catalog); err == nil {
			f.catalog.bytes = len(encoded)
			_ = s.cache.Set(ctx, key, encoded, pickupFailureTTL)
		}
	}
	s.mu.Lock()
	if f.catalog != nil {
		s.remember(key, f.catalog)
	}
	if s.flights[flightKey] == f {
		delete(s.flights, flightKey)
	}
	for _, owner := range f.owners {
		delete(s.selectionOwners, owner)
	}
	close(f.done)
	s.mu.Unlock()
}

func (s *Service) fetchCatalog(
	ctx context.Context,
	key string,
	carrier delivery.Carrier,
	city string,
) (*pickupCatalog, error) {
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
	catalog, err := s.catalogue(ctx, carrier, city, "")
	if err != nil {
		return delivery.PickupArea{}, err
	}
	if regions == nil {
		regions = catalog.index.Regions()
	}
	if len(catalog.Points) > 0 && !catalog.index.SupportsRegions(regions) {
		return delivery.PickupArea{}, delivery.ErrRegionNotAllowed
	}
	if box != nil {
		return catalog.index.Area(*box, regions), nil
	}
	return catalog.index.List(regions, offset, limit), nil
}

func (s *Service) candidates(ctx context.Context, carrier delivery.Carrier) ([]ids.TenantID, error) {
	// База не вызывается под замком: медленная карта не задерживает проверку выбранного ПВЗ.
	s.sourceMu.Lock()
	after := s.sources[carrier]
	s.sourceMu.Unlock()
	tenants, err := s.readSources(ctx, carrier, after)
	if err == nil && len(tenants) > 0 {
		s.sourceMu.Lock()
		// Запоздавший ответ не откатывает уже продвинутый курсор.
		if s.sources[carrier] == after {
			s.sources[carrier] = tenants[0]
		}
		s.sourceMu.Unlock()
	}
	return tenants, err
}

// readSources выбирает источник перед вызовом, не удерживая устаревшую пачку после ожидания другого магазина.
func (s *Service) readSources(
	ctx context.Context,
	carrier delivery.Carrier,
	after ids.TenantID,
) ([]ids.TenantID, error) {
	var tenants []ids.TenantID
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		tenants, err = tx.Participants().PickupSources(ctx, string(carrier), after, 1)
		if err != nil || len(tenants) > 0 || after == "" {
			return err
		}
		tenants, err = tx.Participants().PickupSources(ctx, string(carrier), "", 1)
		return err
	})
	return tenants, err
}

func (s *Service) fetch(
	ctx context.Context,
	carrier delivery.Carrier,
	city string,
	tenants []ids.TenantID,
) ([]delivery.PickupPoint, error) {
	failed := 0
	seen := make(map[ids.TenantID]bool)
	for attempted := 0; attempted < s.config.Attempts && len(tenants) > 0; attempted++ {
		tenant := tenants[0]
		if seen[tenant] {
			break
		}
		seen[tenant] = true
		callCtx, cancel := context.WithTimeout(ctx, ports.PickupCatalogBudget(s.config.QuoteTimeout))
		if ports.PlatformClassFromContext(ctx) != ports.PlatformCheckout {
			callCtx = ports.WithPlatformCallClass(callCtx, ports.PlatformCatalog)
		}
		answer, err := s.platform.PickupPoints(callCtx, tenant, carrier, city)
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil && answer.Available {
			return answer.Items, nil
		}
		if err != nil {
			failed++
		}
		if attempted+1 < s.config.Attempts {
			tenants, err = s.readSources(ctx, carrier, tenant)
			if err != nil {
				return nil, err
			}
		}
	}
	if failed > 0 {
		return nil, ErrSellerUnreachable
	}
	return []delivery.PickupPoint{}, nil
}

// Point проверяет код по списку выбранной службы в городе.
func (s *Service) Point(
	ctx context.Context,
	buyerID ids.UUID,
	carrier delivery.Carrier,
	code, city string,
) (delivery.PickupPoint, error) {
	if buyerID == "" {
		return delivery.PickupPoint{}, identity.ErrUnauthorized
	}
	return s.lookupPoint(ctx, carrier, code, city, buyerID)
}

// ProfilePoint использует общий пул карты: профиль не занимает резерв выбора ПВЗ покупки.
func (s *Service) ProfilePoint(
	ctx context.Context,
	carrier delivery.Carrier,
	code, city string,
) (delivery.PickupPoint, error) {
	return s.lookupPoint(ctx, carrier, code, city, "")
}

func (s *Service) lookupPoint(
	ctx context.Context,
	carrier delivery.Carrier,
	code, city string,
	buyerID ids.UUID,
) (delivery.PickupPoint, error) {
	catalog, err := s.catalogue(ctx, carrier, city, buyerID)
	if err != nil {
		return delivery.PickupPoint{}, err
	}
	for _, point := range catalog.Points {
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
