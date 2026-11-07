package fake

import (
	"context"
	"encoding/json"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type buyerFeaturesData struct {
	Profiles map[ids.UUID]buyer.ProfileState
	Saved    map[ids.UUID]map[ports.SavedKind][]ports.SavedOffer
	Shops    map[ids.UUID]map[ids.TenantID]bool
	Order    map[ids.UUID][]ids.TenantID
}

type buyerFeatures struct {
	d     *buyerFeaturesData
	store *data
}

// GeoIP задаёт ответ геолокации и считает обращения в тестах.
type GeoIP struct {
	CacheHit bool
	Region   *buyer.Region
	Err      error
	Calls    int
}

// Locate — возвращает регион или nil при отсутствии подсказки.
func (g *GeoIP) Locate(_ context.Context, _ string) (*buyer.Region, error) {
	g.Calls++
	return g.Region, g.Err
}

func newBuyerFeaturesData() *buyerFeaturesData {
	return &buyerFeaturesData{
		Order:    map[ids.UUID][]ids.TenantID{},
		Profiles: map[ids.UUID]buyer.ProfileState{},
		Saved:    map[ids.UUID]map[ports.SavedKind][]ports.SavedOffer{},
		Shops:    map[ids.UUID]map[ids.TenantID]bool{},
	}
}

func (d *buyerFeaturesData) clone() *buyerFeaturesData {
	result := newBuyerFeaturesData()
	// Копируем снимки глубоко: откат не должен менять указатели исходной транзакции.
	for id, state := range d.Profiles {
		result.Profiles[id] = buyer.RestoreProfile(state).State()
	}
	for id, kinds := range d.Saved {
		result.Saved[id] = map[ports.SavedKind][]ports.SavedOffer{}
		for kind, items := range kinds {
			for _, item := range items {
				raw, _ := json.Marshal(item.Offer)
				_ = json.Unmarshal(raw, &item.Offer)
				if item.TargetPrice != nil {
					v := *item.TargetPrice
					item.TargetPrice = &v
				}
				result.Saved[id][kind] = append(result.Saved[id][kind], item)
			}
		}
	}
	for id, shops := range d.Shops {
		result.Order[id] = append([]ids.TenantID{}, d.Order[id]...)
		result.Shops[id] = map[ids.TenantID]bool{}
		for tenant, enabled := range shops {
			result.Shops[id][tenant] = enabled
		}
	}
	return result
}

// BuyerFeatures — транзакционное хранилище покупательских данных.
func (t *tx) BuyerFeatures() ports.BuyerFeatures {
	return buyerFeatures{d: t.data.features, store: t.data}
}

// Lock — сериализует изменения списков одного покупателя.
func (r buyerFeatures) Lock(_ context.Context, _ ids.UUID) error { return nil }

// Profile возвращает пустой профиль до первого сохранения.
func (r buyerFeatures) Profile(_ context.Context, id ids.UUID) (*buyer.Profile, error) {
	return buyer.RestoreProfile(r.d.Profiles[id]), nil
}

// SaveProfile — сохраняет профиль только при совпадении исходной версии.
func (r buyerFeatures) SaveProfile(_ context.Context, id ids.UUID, profile *buyer.Profile) error {
	if r.d.Profiles[id].Version != profile.OriginVersion() {
		return buyer.ErrProfileConflict
	}
	r.d.Profiles[id] = profile.State()
	profile.MarkSaved()
	return nil
}

// Saved — читает наблюдения покупателя.
func (r buyerFeatures) Saved(_ context.Context, id ids.UUID) ([]ports.SavedOffer, error) {
	return append([]ports.SavedOffer{}, r.d.Saved[id][ports.PriceWatches]...), nil
}

// PutSaved — добавляет предложение или меняет цель без сброса исходной цены.
func (r buyerFeatures) PutSaved(_ context.Context, id ids.UUID, kind ports.SavedKind, item ports.SavedOffer) error {
	if r.d.Saved[id] == nil {
		r.d.Saved[id] = map[ports.SavedKind][]ports.SavedOffer{}
	}
	for i, old := range r.d.Saved[id][kind] {
		if old.Offer.ID == item.Offer.ID {
			r.d.Saved[id][kind][i].TargetPrice = item.TargetPrice
			return nil
		}
	}
	r.d.Saved[id][kind] = append([]ports.SavedOffer{item}, r.d.Saved[id][kind]...)
	return nil
}

// DeleteSaved — идемпотентно удаляет предложение из списка.
func (r buyerFeatures) DeleteSaved(_ context.Context, id ids.UUID, kind ports.SavedKind, offer string) error {
	for i, old := range r.d.Saved[id][kind] {
		if old.Offer.ID == offer {
			r.d.Saved[id][kind] = append(r.d.Saved[id][kind][:i], r.d.Saved[id][kind][i+1:]...)
			break
		}
	}
	return nil
}

// Subscriptions — возвращает подписки покупателя.
func (r buyerFeatures) Subscriptions(_ context.Context, id ids.UUID) ([]ids.TenantID, error) {
	return append([]ids.TenantID{}, r.d.Order[id]...), nil
}

// Subscribe — идемпотентно меняет подписку под блокировкой покупателя.
func (r buyerFeatures) Subscribe(
	_ context.Context,
	id ids.UUID,
	tenant ids.TenantID,
	enabled ports.SubscriptionState,
) error {
	if r.d.Shops[id] == nil {
		r.d.Shops[id] = map[ids.TenantID]bool{}
	}
	if enabled {
		if !r.d.Shops[id][tenant] {
			r.d.Order[id] = append([]ids.TenantID{tenant}, r.d.Order[id]...)
		}
		r.d.Shops[id][tenant] = true
	} else {
		delete(r.d.Shops[id], tenant)
		for i, key := range r.d.Order[id] {
			if key == tenant {
				r.d.Order[id] = append(r.d.Order[id][:i], r.d.Order[id][i+1:]...)
				break
			}
		}
	}
	return nil
}

// Subscribers — вычисляет число подписчиков и признак текущего покупателя.
func (r buyerFeatures) Subscribers(_ context.Context, tenant ids.TenantID, id ids.UUID) (int, bool, error) {
	count := 0
	for _, shops := range r.d.Shops {
		if shops[tenant] {
			count++
		}
	}
	return count, r.d.Shops[id][tenant], nil
}

func (r buyerFeatures) FavoritesPage(
	ctx context.Context,
	id ids.UUID,
	limit, offset int,
) ([]ports.SavedOffer, int, error) {
	items := r.d.Saved[id][ports.Favorites]
	start := min(offset, len(items))
	end := start + min(limit, len(items)-start)
	return items[start:end], len(items), nil
}

func (r buyerFeatures) PriceDrops(ctx context.Context, id ids.UUID, contexts map[ids.TenantID]string) (int, error) {
	count := 0
	for _, item := range r.d.Saved[id][ports.PriceWatches] {
		key, err := ids.ParseOfferID(item.Offer.ID)
		if err != nil {
			return 0, err
		}
		state, err := (offers{r.store}).Offer(ctx, key.Tenant, key.Product)
		if err != nil {
			continue
		}
		doc, ok := appcatalog.Document(state)
		keyName := contexts[key.Tenant]
		price, found := catalog.SelectPrice(doc.PriceProfiles, keyName)
		ok = ok && found
		doc.Price = price.EffectivePrice.Kopecks()
		if ok && doc.Visible && buyer.PriceDropped(item.PriceAtWatch, money.FromKopecks(doc.Price), item.TargetPrice) {
			count++
		}
	}
	return count, nil
}

func (r buyerFeatures) SavedStats(
	_ context.Context,
	id ids.UUID,
	kind ports.SavedKind,
	offer ids.OfferID,
) (int, bool, error) {
	items := r.d.Saved[id][kind]
	for _, item := range items {
		if item.Offer.ID == offer.String() {
			return len(items), true, nil
		}
	}
	return len(items), false, nil
}

func (r buyerFeatures) UpdateTarget(
	_ context.Context,
	id ids.UUID,
	kind ports.SavedKind,
	offer ids.OfferID,
	target *money.Money,
) error {
	if kind == ports.Favorites {
		return nil
	}
	for i, item := range r.d.Saved[id][kind] {
		if item.Offer.ID == offer.String() {
			r.d.Saved[id][kind][i].TargetPrice = target
			break
		}
	}
	return nil
}

// Cached позволяет проверять, что попадание в кэш не расходует лимит.
func (g *GeoIP) Cached(_ context.Context, _ string) (*buyer.Region, bool) {
	return g.Region, g.CacheHit
}

// LocateUncached имитирует запрос провайдера после промаха кэша.
func (g *GeoIP) LocateUncached(ctx context.Context, ip string) (*buyer.Region, error) {
	return g.Locate(ctx, ip)
}

func (r buyerFeatures) WatchTenants(_ context.Context, id ids.UUID) ([]ids.TenantID, error) {
	tenants := []ids.TenantID{}
	seen := map[ids.TenantID]bool{}
	for _, item := range r.d.Saved[id][ports.PriceWatches] {
		key, err := ids.ParseOfferID(item.Offer.ID)
		if err != nil {
			return nil, err
		}
		if !seen[key.Tenant] {
			tenants = append(tenants, key.Tenant)
			seen[key.Tenant] = true
		}
	}
	return tenants, nil
}
