package postgres

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	getBuyerWatchTenants  = `SELECT DISTINCT tenant_id FROM buyer_price_watches WHERE buyer_id = $1 ORDER BY tenant_id`
	countBuyerFavorites   = `SELECT count(*) FROM buyer_favorites WHERE buyer_id = $1`
	getBuyerFavoritesPage = `SELECT snapshot FROM buyer_favorites
  WHERE buyer_id = $1 ORDER BY created_at DESC, tenant_id, product_id LIMIT $2 OFFSET $3`
	countBuyerPriceDrops = `SELECT count(*) FROM buyer_price_watches w
  JOIN catalog_products p ON w.tenant_id = p.tenant_id AND w.product_id = p.product_id
  JOIN participants pa ON pa.tenant_id = p.tenant_id
  JOIN tenants t ON t.tenant_id = p.tenant_id
  JOIN moderation_verdicts mv ON mv.tenant_id = p.tenant_id AND mv.product_id = p.product_id
  CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(p.price_profiles) = 'array'
  THEN p.price_profiles ELSE '[]'::jsonb END) AS profile(body)
  WHERE w.buyer_id = $1 AND NOT p.deleted AND pa.enabled AND pa.delivery_valid AND t.status = 'active'
  AND mv.status = 'allowed' AND COALESCE((p.card->>'visible')::boolean, false)
  AND COALESCE((p.card->'publication'->>'published')::boolean, false)
  AND EXISTS(SELECT 1 FROM jsonb_array_elements(p.price_profiles) AS public_profile(body)
  WHERE public_profile.body->>'key' = $3)
  AND profile.body->>'key' = $2::jsonb->>p.tenant_id
  AND (profile.body->>'effectivePrice')::bigint < w.price_at_watch
  AND (w.target_price IS NULL OR (profile.body->>'effectivePrice')::bigint <= w.target_price)`
	putBuyerFavorite = `INSERT INTO buyer_favorites(buyer_id, tenant_id, product_id, snapshot)
  VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`

	lockBuyerFeatures  = `SELECT pg_advisory_xact_lock(hashtextextended($1, 39))`
	getBuyerProfile    = `SELECT body, version FROM buyer_profiles WHERE buyer_id = $1`
	insertBuyerProfile = `INSERT INTO buyer_profiles(buyer_id, body, version) VALUES ($1, $2, $3)
  ON CONFLICT DO NOTHING`
	updateBuyerProfile = `UPDATE buyer_profiles
  SET body = $2,
  version = $3
  WHERE buyer_id = $1 AND version = $4`
	getBuyerSaved = `SELECT snapshot, price_at_watch, target_price FROM buyer_price_watches
  WHERE buyer_id = $1 ORDER BY created_at DESC, tenant_id, product_id`
	putBuyerSaved = `INSERT INTO buyer_price_watches(
  buyer_id, tenant_id, product_id, snapshot, price_at_watch, target_price)
  VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT(buyer_id, tenant_id, product_id) DO UPDATE
  SET target_price = EXCLUDED.target_price`
	deleteBuyerSaved      = `DELETE FROM buyer_price_watches WHERE buyer_id = $1 AND tenant_id = $2 AND product_id = $3`
	getBuyerSubscriptions = `SELECT tenant_id FROM buyer_shop_subscriptions WHERE buyer_id = $1
  ORDER BY created_at DESC, tenant_id`
	putBuyerSubscription = `INSERT INTO buyer_shop_subscriptions(buyer_id, tenant_id)
  VALUES ($1, $2) ON CONFLICT DO NOTHING`
	deleteBuyerSubscription = `DELETE FROM buyer_shop_subscriptions WHERE buyer_id = $1 AND tenant_id = $2`
	getShopSubscribers      = `SELECT count(*) FROM buyer_shop_subscriptions WHERE tenant_id = $1`
	isShopSubscriber        = `SELECT EXISTS(SELECT 1 FROM buyer_shop_subscriptions
  WHERE tenant_id = $1 AND buyer_id = $2)`
	deleteBuyerFavorite = `DELETE FROM buyer_favorites WHERE buyer_id = $1 AND tenant_id = $2 AND product_id = $3`
	favoriteStats       = `SELECT count(*), EXISTS(SELECT 1 FROM buyer_favorites
  WHERE buyer_id = $1 AND tenant_id = $2 AND product_id = $3) FROM buyer_favorites WHERE buyer_id = $1`
	watchStats = `SELECT count(*), EXISTS(SELECT 1 FROM buyer_price_watches
  WHERE buyer_id = $1 AND tenant_id = $2 AND product_id = $3) FROM buyer_price_watches WHERE buyer_id = $1`
	updateWatchTarget = `UPDATE buyer_price_watches SET target_price = $4
  WHERE buyer_id = $1 AND tenant_id = $2 AND product_id = $3`
)

// BuyerFeatures — транзакционное хранилище покупательских данных.
type BuyerFeatures struct {
	db *gorm.DB
}

// BuyerFeatures возвращает репозиторий текущей транзакции.
func (s *scope) BuyerFeatures() ports.BuyerFeatures { return &BuyerFeatures{db: s.db} }

// Lock — сериализует изменения списков одного покупателя.
func (r *BuyerFeatures) Lock(ctx context.Context, id ids.UUID) error {
	_, err := execSQL(ctx, r.db, lockBuyerFeatures, string(id))
	return err
}

// Profile возвращает пустой профиль до первого сохранения.
func (r *BuyerFeatures) Profile(ctx context.Context, id ids.UUID) (*buyer.Profile, error) {
	var raw []byte
	var state buyer.ProfileState
	err := rawSQL(ctx, r.db, getBuyerProfile, string(id)).Row().Scan(&raw, &state.Version)
	if notFound(err) {
		return buyer.RestoreProfile(state), nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	return buyer.RestoreProfile(state), nil
}

// SaveProfile — сохраняет профиль только при совпадении исходной версии.
func (r *BuyerFeatures) SaveProfile(ctx context.Context, id ids.UUID, profile *buyer.Profile) error {
	state := profile.State()
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	query := updateBuyerProfile
	args := []any{string(id), raw, state.Version, profile.OriginVersion()}
	if profile.OriginVersion() == 0 {
		query = insertBuyerProfile
		args = args[:3]
	}
	affected, err := execSQL(ctx, r.db, query, args...)
	if err != nil {
		return err
	}
	if affected != 1 {
		return buyer.ErrProfileConflict
	}
	profile.MarkSaved()
	return nil
}

// Saved — читает наблюдения покупателя.
func (r *BuyerFeatures) Saved(ctx context.Context, id ids.UUID) ([]ports.SavedOffer, error) {
	rows, err := rawSQL(ctx, r.db, getBuyerSaved, string(id)).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := []ports.SavedOffer{}
	for rows.Next() {
		var item ports.SavedOffer
		var raw []byte
		var initial int64
		var target *int64
		if err := rows.Scan(&raw, &initial, &target); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &item.Offer); err != nil {
			return nil, err
		}
		item.PriceAtWatch = money.FromKopecks(initial)
		if target != nil {
			value := money.FromKopecks(*target)
			item.TargetPrice = &value
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// PutSaved — добавляет предложение или меняет цель без сброса исходной цены.
func (r *BuyerFeatures) PutSaved(
	ctx context.Context,
	id ids.UUID,
	kind ports.SavedKind,
	item ports.SavedOffer,
) error {
	key, err := ids.ParseOfferID(item.Offer.ID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(item.Offer)
	if err != nil {
		return err
	}
	if kind == ports.Favorites {
		_, err := execSQL(ctx, r.db, putBuyerFavorite, string(id), string(key.Tenant), string(key.Product), raw)
		return err
	}
	var target *int64
	if item.TargetPrice != nil {
		value := item.TargetPrice.Kopecks()
		target = &value
	}
	_, err = execSQL(
		ctx,
		r.db,
		putBuyerSaved,
		string(id),
		string(key.Tenant),
		string(key.Product),
		raw,
		item.PriceAtWatch.Kopecks(),
		target,
	)
	return err
}

// DeleteSaved — идемпотентно удаляет предложение из списка.
func (r *BuyerFeatures) DeleteSaved(ctx context.Context, id ids.UUID, kind ports.SavedKind, offer string) error {
	key, err := ids.ParseOfferID(offer)
	if err != nil {
		return err
	}
	query := deleteBuyerSaved
	if kind == ports.Favorites {
		query = deleteBuyerFavorite
	}
	_, err = execSQL(ctx, r.db, query, string(id), string(key.Tenant), string(key.Product))
	return err
}

// Subscriptions — возвращает подписки покупателя.
func (r *BuyerFeatures) Subscriptions(ctx context.Context, id ids.UUID) ([]ids.TenantID, error) {
	rows, err := rawSQL(ctx, r.db, getBuyerSubscriptions, string(id)).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := []ids.TenantID{}
	for rows.Next() {
		var tenant string
		if err := rows.Scan(&tenant); err != nil {
			return nil, err
		}
		result = append(result, ids.TenantID(tenant))
	}
	return result, rows.Err()
}

// Subscribe — идемпотентно меняет подписку под блокировкой покупателя.
func (r *BuyerFeatures) Subscribe(
	ctx context.Context,
	id ids.UUID,
	tenant ids.TenantID,
	enabled ports.SubscriptionState,
) error {
	query := putBuyerSubscription
	if !enabled {
		query = deleteBuyerSubscription
	}
	_, err := execSQL(ctx, r.db, query, string(id), string(tenant))
	return err
}

// Subscribers — вычисляет число подписчиков и признак текущего покупателя.
func (r *BuyerFeatures) Subscribers(ctx context.Context, tenant ids.TenantID, id ids.UUID) (int, bool, error) {
	var count int
	var subscribed bool
	err := rawSQL(ctx, r.db, getShopSubscribers, string(tenant)).Row().Scan(&count)
	if err == nil && id != "" {
		err = rawSQL(ctx, r.db, isShopSubscriber, string(tenant), string(id)).Row().Scan(&subscribed)
	}
	return count, subscribed, err
}

// FavoritesPage не читает снимки за пределами страницы.
func (r *BuyerFeatures) FavoritesPage(
	ctx context.Context,
	id ids.UUID,
	limit, offset int,
) ([]ports.SavedOffer, int, error) {
	var total int
	if err := rawSQL(ctx, r.db, countBuyerFavorites, string(id)).Row().Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := rawSQL(ctx, r.db, getBuyerFavoritesPage, string(id), limit, offset).Rows()
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := []ports.SavedOffer{}
	for rows.Next() {
		var raw []byte
		var item ports.SavedOffer
		if err := rows.Scan(&raw); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(raw, &item.Offer); err != nil {
			return nil, 0, err
		}
		result = append(result, item)
	}
	return result, total, rows.Err()
}

// PriceDrops считает действующие цены без чтения сохранённых карточек.
func (r *BuyerFeatures) PriceDrops(ctx context.Context, id ids.UUID, contexts map[ids.TenantID]string) (int, error) {
	var count int
	raw, err := json.Marshal(contexts)
	if err != nil {
		return 0, err
	}
	err = rawSQL(ctx, r.db, countBuyerPriceDrops, string(id), raw, catalog.DefaultPriceProfile).Row().Scan(&count)
	return count, err
}

// SavedStats проверяет лимит и наличие без чтения снимков.
func (r *BuyerFeatures) SavedStats(
	ctx context.Context,
	id ids.UUID,
	kind ports.SavedKind,
	offer ids.OfferID,
) (int, bool, error) {
	query := favoriteStats
	if kind == ports.PriceWatches {
		query = watchStats
	}
	var count int
	var exists bool
	err := rawSQL(ctx, r.db, query, string(id), string(offer.Tenant), string(offer.Product)).Row().Scan(&count, &exists)
	return count, exists, err
}

// UpdateTarget сохраняет исходную цену и снимок существующей записи.
func (r *BuyerFeatures) UpdateTarget(
	ctx context.Context,
	id ids.UUID,
	kind ports.SavedKind,
	offer ids.OfferID,
	target *money.Money,
) error {
	if kind == ports.Favorites {
		return nil
	}
	var price *int64
	if target != nil {
		value := target.Kopecks()
		price = &value
	}
	_, err := execSQL(ctx, r.db, updateWatchTarget, string(id), string(offer.Tenant), string(offer.Product), price)
	return err
}

// WatchTenants читает только магазины наблюдений, без снимков товаров.
func (r *BuyerFeatures) WatchTenants(ctx context.Context, id ids.UUID) ([]ids.TenantID, error) {
	rows, err := rawSQL(ctx, r.db, getBuyerWatchTenants, string(id)).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	tenants := []ids.TenantID{}
	for rows.Next() {
		var tenant string
		if err := rows.Scan(&tenant); err != nil {
			return nil, err
		}
		tenants = append(tenants, ids.TenantID(tenant))
	}
	return tenants, rows.Err()
}
