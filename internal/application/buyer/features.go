package buyer

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
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
	// RegionIP — локальная подсказка по адресу клиента.
	RegionIP = "ip"
)

// Features — покупательские сценарии в одной транзакции на изменение.
type Features struct {
	profileWrites sync.Map
	listWrites    sync.Map
	points        ports.ProfilePickupLookup
	uow           ports.UnitOfWork
	detector      ports.CityDetector
}

// Alerts — счётчик достигнутых ценовых условий.
type Alerts struct {
	PriceDrops int
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

// NewFeatures — собирает сценарии; nil detector отключает IP-подсказку.
func NewFeatures(uow ports.UnitOfWork, detector ports.CityDetector, points ports.ProfilePickupLookup) *Features {
	return &Features{uow: uow, detector: detector, points: points}
}

// Profile возвращает сохранённый профиль и актуальное число снижений.
func (s *Features) Profile(ctx context.Context, id ids.UUID) (ProfileView, error) {
	var result ProfileView
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.BuyerFeatures().Profile(ctx, id)
		if err != nil {
			return err
		}
		result.ProfileState = p.State()
		result.Alerts.PriceDrops, err = tx.BuyerFeatures().PriceDrops(ctx, id)
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
	release, err := s.beginProfileWrite(id)
	if err != nil {
		return ProfileView{}, err
	}
	defer release()
	var original *buyer.Profile
	err = s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		profile, err := tx.BuyerFeatures().Profile(ctx, id)
		if err != nil {
			return err
		}
		original = profile
		return nil
	})
	if err != nil {
		return ProfileView{}, err
	}
	previous := original.State()
	state, err := change(original.State())
	if err != nil {
		return ProfileView{}, err
	}
	if selected, saved := state.PickupPoint, previous.PickupPoint; selected != nil && saved != nil &&
		selected.Provider == saved.Provider && selected.Code == saved.Code && selected.City == saved.City {
		state.PickupPoint = saved
	}
	if state.PickupPoint != nil &&
		(previous.PickupPoint == nil || *state.PickupPoint != *previous.PickupPoint) {
		selected := state.PickupPoint
		if _, err := delivery.NormalizeCity(selected.City); err != nil {
			return ProfileView{}, err
		}
		point, err := s.points.ProfilePoint(ctx, delivery.Carrier(selected.Provider), selected.Code, selected.City)
		if err != nil {
			return ProfileView{}, err
		}
		state.PickupPoint = &buyer.Pickup{
			Provider:   string(point.Carrier),
			Code:       point.Code,
			City:       point.City,
			Address:    point.Address,
			RegionCode: point.RegionCode,
		}
	}
	var result ProfileView
	err = s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.BuyerFeatures().Profile(ctx, id)
		if err != nil {
			return err
		}
		if p.State().Version != previous.Version {
			return buyer.ErrProfileConflict
		}
		changed, err := p.Change(state)
		if err != nil {
			return err
		}
		drops, err := tx.BuyerFeatures().PriceDrops(ctx, id)
		if err != nil {
			return err
		}
		if changed {
			// Короткая блокировка включает только запись по версии и фиксацию.
			if err := tx.BuyerFeatures().TryLockProfile(ctx, id); err != nil {
				return err
			}
			if err := tx.BuyerFeatures().SaveProfile(ctx, id, p); err != nil {
				return err
			}
		}
		result.ProfileState = p.State()
		result.Alerts.PriceDrops = drops
		return nil
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
	if result.Source != RegionNone || s.detector == nil {
		return result, nil
	}
	return s.regionByIP(ctx, ip), nil
}

func (s *Features) regionByIP(ctx context.Context, ip string) RegionView {
	city, err := s.detector.DetectCity(ctx, ip)
	if err != nil {
		// Отказ локальной базы оставляет покупателю ручной выбор региона.
		return RegionView{Source: RegionNone}
	}
	name, ok := regions.Name(city.RegionCode)
	if !ok {
		return RegionView{Source: RegionNone}
	}
	return regionView(buyer.Region{Code: city.RegionCode, Name: name}, RegionIP)
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
	return s.savedOfferViews(ctx, saved)
}

func (s *Features) savedOfferViews(
	ctx context.Context,
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
	current := map[string]ports.OfferDocument{}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		states, err := tx.Offers().Batch(ctx, keys)
		if err != nil {
			return err
		}
		for _, state := range states {
			if doc, ok := pricedDocument(state); ok {
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
	return s.writeLists(ctx, id, func(ctx context.Context, tx ports.Tx) error {
		repo := tx.BuyerFeatures()
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
		doc, ok := pricedDocument(state)
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
	return s.writeLists(ctx, id, func(ctx context.Context, tx ports.Tx) error {
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
	return s.writeLists(ctx, id, func(ctx context.Context, tx ports.Tx) error {
		repo := tx.BuyerFeatures()
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
	result, err := s.savedOfferViews(ctx, saved)
	return result, total, err
}

func pricedDocument(state ports.OfferState) (ports.OfferDocument, bool) {
	doc, ok := appcatalog.Document(state)
	if !ok || !doc.Visible {
		return doc, false
	}
	return doc, true
}

// beginProfileWrite отсекает параллельное изменение профиля в этой реплике до первого чтения.
func (s *Features) beginProfileWrite(id ids.UUID) (func(), error) {
	if _, busy := s.profileWrites.LoadOrStore(id, struct{}{}); busy {
		return nil, buyer.ErrFeaturesBusy
	}
	return func() { s.profileWrites.Delete(id) }, nil
}

// writeLists сериализует запросы покупателя в процессе, а между репликами
// повторяет попытку только после возврата соединения в пул.
func (s *Features) writeLists(
	ctx context.Context,
	id ids.UUID,
	change func(context.Context, ports.Tx) error,
) error {
	done := make(chan struct{})
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		previous, busy := s.listWrites.LoadOrStore(id, done)
		if !busy {
			break
		}
		select {
		case <-previous.(chan struct{}):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() {
		s.listWrites.Delete(id)
		close(done)
	}()

	for delay := 5 * time.Millisecond; ; delay = min(2*delay, 40*time.Millisecond) {
		err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			if err := tx.BuyerFeatures().TryLockLists(ctx, id); err != nil {
				return err
			}
			return change(ctx, tx)
		})
		if !errors.Is(err, buyer.ErrFeaturesBusy) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}
