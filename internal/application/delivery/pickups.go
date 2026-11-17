// Package delivery получает пункты выдачи через магазины.
package delivery

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
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
	uow      ports.UnitOfWork
	platform ports.PlatformAPI
	cache    ports.PickupCache
	config   Config
	mu       sync.Mutex
	flights  map[string]*flight
}

type flight struct {
	cancel  context.CancelFunc
	waiters int
	done    chan struct{}
	points  []delivery.PickupPoint
	err     error
}

// NewService собирает поиск пунктов и общий кэш.
func NewService(uow ports.UnitOfWork, platform ports.PlatformAPI, cache ports.PickupCache, cfg Config) *Service {
	return &Service{uow: uow, platform: platform, cache: cache, config: cfg, flights: make(map[string]*flight)}
}

// Carriers — службы, поддерживаемые контрактом настроек магазинов.
func (s *Service) Carriers() []delivery.CarrierInfo {
	return []delivery.CarrierInfo{{Code: "cdek", Name: "СДЭК"}, {Code: "yandex", Name: "Яндекс Доставка"}}
}

// Search возвращает первый успешный список; отказ службы позволяет перейти к следующему магазину.
func (s *Service) Search(ctx context.Context, carrier delivery.Carrier, city string) ([]delivery.PickupPoint, error) {
	return s.search(ctx, carrier, city, nil)
}

// SearchFor ограничивает источник магазинами текущей покупки.
func (s *Service) SearchFor(
	ctx context.Context,
	carrier delivery.Carrier,
	city string,
	tenants []ids.TenantID,
) ([]delivery.PickupPoint, error) {
	if len(tenants) == 0 {
		return []delivery.PickupPoint{}, nil
	}
	return s.search(ctx, carrier, city, tenants)
}

func (s *Service) search(
	ctx context.Context,
	carrier delivery.Carrier,
	city string,
	tenants []ids.TenantID,
) ([]delivery.PickupPoint, error) {
	if _, err := delivery.ParseCarrier(string(carrier)); err != nil {
		return nil, err
	}
	city, err := delivery.NormalizeCity(city)
	if err != nil {
		return nil, err
	}
	candidates, err := s.candidates(ctx, carrier, tenants)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return []delivery.PickupPoint{}, nil
	}
	key := "v2:" + string(carrier) + ":" + strings.ToLower(city)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	f, ok := s.flights[key]
	if !ok {
		// Общая загрузка живёт, пока есть ожидающие, и имеет конечный общий срок.
		budget := time.Duration(s.config.Attempts)*s.config.QuoteTimeout + 5*time.Second
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
		f = &flight{done: make(chan struct{}), cancel: cancel}
		s.flights[key] = f
		go s.load(loadCtx, key, carrier, city, candidates, f)
	}
	f.waiters++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		f.waiters--
		if f.waiters == 0 {
			f.cancel()
			if s.flights[key] == f {
				delete(s.flights, key)
			}
		}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.done:
		return slices.Clone(f.points), f.err
	}
}

func (s *Service) load(
	ctx context.Context,
	key string,
	carrier delivery.Carrier,
	city string,
	tenants []ids.TenantID,
	f *flight,
) {
	defer f.cancel()
	f.points, f.err = s.cached(ctx, key, carrier, city, tenants)
	s.mu.Lock()
	if s.flights[key] == f {
		delete(s.flights, key)
	}
	close(f.done)
	s.mu.Unlock()
}

func (s *Service) cached(
	ctx context.Context,
	key string,
	carrier delivery.Carrier,
	city string,
	tenants []ids.TenantID,
) ([]delivery.PickupPoint, error) {
	if value, hit, err := s.cache.Get(ctx, key); err == nil && hit {
		var points []delivery.PickupPoint
		if json.Unmarshal(value, &points) == nil && len(points) > 0 {
			return points, nil
		}
	}
	points, err := s.fetch(ctx, carrier, city, tenants)
	if err == nil && len(points) > 0 {
		if value, marshalErr := json.Marshal(points); marshalErr == nil {
			// Кэш необязателен: отказ Redis не скрывает успешный ответ магазина.
			_ = s.cache.Set(ctx, key, value, s.config.TTL)
		}
	}
	return points, err
}

func (s *Service) candidates(
	ctx context.Context,
	carrier delivery.Carrier,
	filter []ids.TenantID,
) ([]ids.TenantID, error) {
	var tenants []ids.TenantID
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		keys := filter
		if keys == nil {
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
	for _, tenant := range tenants {
		if attempted >= s.config.Attempts {
			break
		}
		attempted++
		callCtx, cancel := context.WithTimeout(ctx, s.config.QuoteTimeout)
		callCtx = ports.WithPlatformCallClass(callCtx, ports.PlatformCheckout)
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
	tenants ...ids.TenantID,
) (delivery.PickupPoint, error) {
	points, err := s.search(ctx, carrier, city, tenants)
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
