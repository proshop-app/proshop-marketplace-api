package buyer

import (
	"context"
	"log/slog"
	"slices"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/ipnet"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
	"github.com/proshop-app/proshop-marketplace/pkg/regions"
)

const (
	// RegionNone — доступной подсказки нет.
	RegionNone = "none"
	// RegionProfile — регион выбран в профиле.
	RegionProfile = "profile"
	// RegionPickup — регион сохранённого пункта выдачи.
	RegionPickup = "pickup"
	// RegionIP — подсказка провайдера по адресу клиента.
	RegionIP            = "ip"
	regionRequestLimit  = 30
	regionRequestWindow = time.Minute
)

// RegionLimitError передаёт транспорту время до открытия окна лимитера.
type RegionLimitError struct {
	RetryAfter time.Duration
}

// Features — покупательские сценарии в одной транзакции на изменение.
type Features struct {
	uow     ports.UnitOfWork
	geo     ports.GeoIP
	pricing ports.CatalogPricing
	limiter ports.RateLimiter
}

// Alerts — счётчик достигнутых ценовых условий.
type Alerts struct {
	PriceDrops *int
}

// ProfileView — профиль с вычисленным счётчиком уведомлений.
type ProfileView struct {
	buyer.ProfileState
	Alerts Alerts
}

// RegionView — регион и источник подсказки; none содержит null code/name.
type RegionView struct {
	Code   *string `json:"code"`
	Name   *string `json:"name"`
	Source string  `json:"source"`
}

// SavedView — сохранённое предложение с актуальной ценой из проекции.
type SavedView struct {
	Offer        ports.OfferDocument
	Listed       bool
	PriceAtWatch int64
	CurrentPrice *int64
	TargetPrice  *int64
	Dropped      bool
}

// NewFeatures — собирает сценарии; nil GeoIP отключает IP-подсказку.
func NewFeatures(
	uow ports.UnitOfWork,
	geo ports.GeoIP,
	pricing ports.CatalogPricing,
	limiter ports.RateLimiter,
) *Features {
	return &Features{uow: uow, geo: geo, pricing: pricing, limiter: limiter}
}

// Profile возвращает сохранённый профиль и актуальное число снижений.
func (s *Features) Profile(ctx context.Context, id ids.UUID) (ProfileView, error) {
	contexts := s.profileContexts(ctx, id)
	var result ProfileView
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.BuyerFeatures().Profile(ctx, id)
		if err != nil {
			return err
		}
		result.ProfileState = p.State()
		result.Alerts.PriceDrops, err = s.priceDrops(ctx, tx, id, contexts)
		return err
	})
	return result, err
}

// Patch применяет только переданные поля; null очищает регион или пункт.
func (s *Features) Patch(
	ctx context.Context,
	id ids.UUID,
	change func(buyer.ProfileState) (buyer.ProfileState, error),
) (ProfileView, error) {
	contexts := s.profileContexts(ctx, id)
	var result ProfileView
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.BuyerFeatures().Lock(ctx, id); err != nil {
			return err
		}
		p, err := tx.BuyerFeatures().Profile(ctx, id)
		if err != nil {
			return err
		}
		previous := p.State()
		state, err := change(p.State())
		if err != nil {
			return err
		}
		if state.PickupPoint != nil &&
			(previous.PickupPoint == nil || *state.PickupPoint != *previous.PickupPoint) {
			selected := state.PickupPoint
			point, err := tx.PickupPoints().Get(ctx, delivery.Carrier(selected.Provider), selected.Code)
			if err != nil {
				return err
			}
			if !point.Active {
				return delivery.ErrPickupPointNotFound
			}
			state.PickupPoint = &buyer.Pickup{
				Provider:   string(point.Carrier),
				Code:       point.Code,
				Address:    point.Address,
				RegionCode: point.RegionCode,
			}
		}
		changed, err := p.Change(state)
		if err != nil {
			return err
		}
		if changed {
			if err := tx.BuyerFeatures().SaveProfile(ctx, id, p); err != nil {
				return err
			}
		}
		result.ProfileState = p.State()
		result.Alerts.PriceDrops, err = s.priceDrops(ctx, tx, id, contexts)
		return err
	})
	return result, err
}

// Region выбирает подсказку: профиль, сохранённый ПВЗ, затем IP.
func (s *Features) Region(ctx context.Context, id ids.UUID, ip string) (RegionView, error) {
	result := RegionView{Source: RegionNone}
	if id != "" {
		err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			p, err := tx.BuyerFeatures().Profile(ctx, id)
			if err != nil {
				return err
			}
			state := p.State()
			if state.Region != nil {
				result = regionView(*state.Region, RegionProfile)
			} else if state.PickupPoint != nil {
				code := state.PickupPoint.RegionCode
				if name, ok := regions.Name(code); ok {
					result = regionView(buyer.Region{Code: code, Name: name}, RegionPickup)
				}
			}
			return nil
		})
		if err != nil {
			return result, err
		}
	}
	if result.Source != RegionNone || s.geo == nil || ipnet.Network(ip) == "" {
		return result, nil
	}
	locate := s.geo.Locate
	if cache, ok := s.geo.(ports.GeoIPCache); ok {
		if region, hit := cache.Cached(ctx, ip); hit {
			return geoRegion(region), nil
		}
		locate = cache.LocateUncached
	}
	if id == "" {
		key := "region:" + ipnet.Network(ip)
		allowed, retryAfter, err := s.limiter.Allow(ctx, key, regionRequestLimit, regionRequestWindow)
		if err != nil {
			slog.WarnContext(ctx, "Сбой лимитера подсказки региона", "error", err)
		} else if !allowed {
			return result, &RegionLimitError{RetryAfter: retryAfter}
		}
	}
	region, err := locate(ctx, ip)
	if err != nil {
		// Геолокация — подсказка: сбой провайдера не блокирует витрину, вид сбоя пишет адаптер.
		return result, nil //nolint:nilerr // ответ без региона — штатный исход подсказки
	}
	if region == nil {
		return result, nil
	}
	name, ok := regions.Name(region.Code)
	if !ok {
		return result, nil
	}
	return regionView(buyer.Region{Code: region.Code, Name: name}, RegionIP), nil
}

func regionView(region buyer.Region, source string) RegionView {
	return RegionView{Code: &region.Code, Name: &region.Name, Source: source}
}

// Saved — читает наблюдения покупателя.
func (s *Features) Saved(ctx context.Context, id ids.UUID) ([]SavedView, error) {
	var saved []ports.SavedOffer
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		saved, err = tx.BuyerFeatures().Saved(ctx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.savedOfferViews(ctx, id, saved)
}

func (s *Features) savedOfferViews(
	ctx context.Context,
	id ids.UUID,
	saved []ports.SavedOffer,
) ([]SavedView, error) {
	if len(saved) == 0 {
		return []SavedView{}, nil
	}
	keys := make([]ids.OfferID, 0, len(saved))
	for _, item := range saved {
		key, err := ids.ParseOfferID(item.Offer.ID)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	contexts, err := appcatalog.PriceContexts(ctx, s.uow, s.pricing, id, nil)
	if err != nil {
		return nil, err
	}
	current := map[string]ports.OfferDocument{}
	err = s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		states, err := tx.Offers().Batch(ctx, keys)
		if err != nil {
			return err
		}
		for _, state := range states {
			if doc, ok := pricedDocument(state, contexts); ok {
				current[doc.ID] = doc
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]SavedView, 0, len(saved))
	for _, item := range saved {
		view := SavedView{Offer: item.Offer.Document(), PriceAtWatch: item.PriceAtWatch.Kopecks()}
		if item.TargetPrice != nil {
			value := item.TargetPrice.Kopecks()
			view.TargetPrice = &value
		}
		if doc, ok := current[item.Offer.ID]; ok && doc.Visible {
			view.Offer = doc
			view.Listed = true
			price := doc.Price
			view.CurrentPrice = &price
			view.Dropped = buyer.PriceDropped(item.PriceAtWatch, money.FromKopecks(price), item.TargetPrice)
		}
		result = append(result, view)
	}
	return result, nil
}

// PutSaved — добавляет предложение или меняет цель без сброса исходной цены.
func (s *Features) PutSaved(
	ctx context.Context,
	id ids.UUID,
	kind ports.SavedKind,
	offer ids.OfferID,
	target *money.Money,
) error {
	if target != nil && target.Kopecks() < 0 {
		return buyer.ErrInvalidTarget
	}
	contexts, err := appcatalog.PriceContexts(ctx, s.uow, s.pricing, id, []ids.TenantID{offer.Tenant})
	if err != nil {
		return err
	}
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		repo := tx.BuyerFeatures()
		if err := repo.Lock(ctx, id); err != nil {
			return err
		}
		count, exists, err := repo.SavedStats(ctx, id, kind, offer)
		if err != nil {
			return err
		}
		if exists {
			return repo.UpdateTarget(ctx, id, kind, offer, target)
		}
		limit := buyer.FavoritesLimit
		if kind == ports.PriceWatches {
			limit = buyer.PriceWatchesLimit
		}
		if count >= limit {
			return buyer.ErrCollectionLimit
		}
		state, err := tx.Offers().Offer(ctx, offer.Tenant, offer.Product)
		if err != nil {
			return err
		}
		doc, ok := pricedDocument(state, contexts)
		if !ok || !doc.Visible {
			return catalog.ErrProductNotFound
		}
		return repo.PutSaved(ctx, id, kind, ports.SavedOffer{
			Offer:        ports.Snapshot(doc),
			PriceAtWatch: money.FromKopecks(doc.Price),
			TargetPrice:  target,
		})
	})
}

// DeleteSaved — идемпотентно удаляет предложение из списка.
func (s *Features) DeleteSaved(ctx context.Context, id ids.UUID, kind ports.SavedKind, offer string) error {
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.BuyerFeatures().Lock(ctx, id); err != nil {
			return err
		}
		return tx.BuyerFeatures().DeleteSaved(ctx, id, kind, offer)
	})
}

// Subscribe — идемпотентно меняет подписку под блокировкой покупателя.
func (s *Features) Subscribe(
	ctx context.Context,
	id ids.UUID,
	tenant ids.TenantID,
	enabled ports.SubscriptionState,
) error {
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		repo := tx.BuyerFeatures()
		if err := repo.Lock(ctx, id); err != nil {
			return err
		}
		if enabled {
			saved, err := repo.Subscriptions(ctx, id)
			if err != nil {
				return err
			}
			if slices.Contains(saved, tenant) {
				return nil
			}
			if len(saved) >= buyer.SubscriptionsLimit {
				return buyer.ErrCollectionLimit
			}
			record, err := tx.StoreCards().Get(ctx, tenant)
			if err != nil {
				return err
			}
			if !record.PagePublic {
				return participant.ErrNotFound
			}
		}
		return repo.Subscribe(ctx, id, tenant, enabled)
	})
}

// Subscriptions — возвращает подписки покупателя.
func (s *Features) Subscriptions(ctx context.Context, id ids.UUID) ([]ports.ShopCard, error) {
	result := []ports.ShopCard{}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenants, err := tx.BuyerFeatures().Subscriptions(ctx, id)
		if err != nil {
			return err
		}
		result, err = tx.StoreCards().BatchPublic(ctx, tenants)
		return err
	})
	return result, err
}

// Subscribers — вычисляет число подписчиков и признак текущего покупателя.
func (s *Features) Subscribers(ctx context.Context, tenant ids.TenantID, id ids.UUID) (int, *bool, error) {
	var count int
	var subscribed *bool
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		n, enabled, err := tx.BuyerFeatures().Subscribers(ctx, tenant, id)
		count = n
		if id != "" {
			subscribed = &enabled
		}
		return err
	})
	return count, subscribed, err
}

// Favorites возвращает только запрошенную страницу и общее число записей.
func (s *Features) Favorites(ctx context.Context, id ids.UUID, limit, offset int) ([]SavedView, int, error) {
	var saved []ports.SavedOffer
	var total int
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		saved, total, err = tx.BuyerFeatures().FavoritesPage(ctx, id, limit, offset)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	result, err := s.savedOfferViews(ctx, id, saved)
	return result, total, err
}

func (s *Features) profileContexts(ctx context.Context, id ids.UUID) map[ids.TenantID]string {
	var tenants []ids.TenantID
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		tenants, err = tx.BuyerFeatures().WatchTenants(ctx, id)
		return err
	})
	if err != nil {
		slog.WarnContext(ctx, "Магазины наблюдений недоступны", "buyerId", id, "error", err)
		return nil
	}
	if len(tenants) == 0 {
		return map[ids.TenantID]string{}
	}
	contexts, err := appcatalog.PriceContexts(ctx, s.uow, s.pricing, id, tenants)
	if err != nil {
		slog.WarnContext(ctx, "Ценовые контексты профиля недоступны", "buyerId", id, "error", err)
		return nil
	}
	return contexts
}

func (s *Features) priceDrops(
	ctx context.Context,
	tx ports.Tx,
	id ids.UUID,
	contexts map[ids.TenantID]string,
) (*int, error) {
	if contexts == nil {
		return nil, nil
	}
	if len(contexts) == 0 {
		count := 0
		return &count, nil
	}
	count, err := tx.BuyerFeatures().PriceDrops(ctx, id, contexts)
	return &count, err
}

func pricedDocument(state ports.OfferState, contexts map[ids.TenantID]string) (ports.OfferDocument, bool) {
	doc, ok := appcatalog.Document(state)
	if !ok || !doc.Visible {
		return doc, false
	}
	key := contexts[ids.TenantID(doc.TenantID)]
	price, ok := catalog.SelectPrice(doc.PriceProfiles, key)
	if !ok {
		return doc, false
	}
	doc.Price = price.EffectivePrice.Kopecks()
	doc.BasePrice = price.Price.Kopecks()
	return doc, true
}

func geoRegion(region *buyer.Region) RegionView {
	if region != nil {
		if name, ok := regions.Name(region.Code); ok {
			return regionView(buyer.Region{Code: region.Code, Name: name}, RegionIP)
		}
	}
	return RegionView{Source: RegionNone}
}

func (e *RegionLimitError) Error() string {
	return "превышен лимит подсказок региона, повторите через " + e.RetryAfter.Round(time.Second).String()
}
func (e *RegionLimitError) Unwrap() error { return buyer.ErrRegionLimit.WithMessage(e.Error()) }
