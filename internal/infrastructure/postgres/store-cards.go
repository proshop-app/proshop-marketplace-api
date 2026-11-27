package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	getAllStoreCardCacheVersion = `SELECT version FROM store_card_cache_generation WHERE id = 1`
	getStoreCardCacheVersion    = `SELECT COALESCE(sum(version), 0)::bigint FROM store_card_cache_versions
        WHERE tenant_id = ANY($1::text[])`
	batchPublicCards = `SELECT card FROM store_cards WHERE tenant_id = ANY($1) AND page_public
		ORDER BY array_position($1::text[], tenant_id)`
	lockStoreSummary = `SELECT pg_advisory_xact_lock(hashtextextended($1, 38))`
	saveStoreSummary = `
        WITH saved AS (
            INSERT INTO store_cards (
                tenant_id, slug, name, offers, from_price, public, summary, header, card, profile_version,
                page_public, cache_signature
            )
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
            ON CONFLICT (tenant_id) DO UPDATE SET
                slug = EXCLUDED.slug,
                name = EXCLUDED.name,
                offers = EXCLUDED.offers,
                from_price = EXCLUDED.from_price,
                public = EXCLUDED.public,
                cache_signature = EXCLUDED.cache_signature,
                page_public = EXCLUDED.page_public,
                summary = CASE WHEN store_cards.profile_version > EXCLUDED.profile_version
                    THEN jsonb_set(EXCLUDED.summary, '{hidden}', store_cards.summary->'hidden')
                    ELSE EXCLUDED.summary
                END,
                header = CASE WHEN store_cards.profile_version > EXCLUDED.profile_version
                    THEN EXCLUDED.header || jsonb_build_object('logo', store_cards.header->'logo')
                    ELSE EXCLUDED.header
                END,
                card = CASE WHEN store_cards.profile_version > EXCLUDED.profile_version
                    THEN EXCLUDED.card || jsonb_build_object(
                        'logo', store_cards.card->'logo',
                        'mark', store_cards.card->'mark'
                    )
                    ELSE EXCLUDED.card
                END,
                profile_version = GREATEST(store_cards.profile_version, EXCLUDED.profile_version)
            RETURNING tenant_id, slug, name, offers, public
        )
        SELECT record_shop_directory_change(tenant_id,
            CASE WHEN public THEN shop_directory_card(tenant_id, slug, name, offers) END)
        FROM saved`
	deleteStoreCategories = `DELETE FROM store_card_categories WHERE tenant_id = $1`
	insertStoreCategories = `INSERT INTO store_card_categories (category_id, tenant_id, name, count)
        SELECT id, $1, name, count FROM jsonb_to_recordset($2::jsonb) AS c(id text, name text, count integer)`
	getStoreSummary = `SELECT summary, header, card, public, profile_version, page_public
		FROM store_cards WHERE tenant_id = $1`
	getShopHeader = `SELECT header
		FROM store_cards WHERE slug = $1 AND page_public`
	listShopCards = `
		WITH filtered AS (
		SELECT s.card, s.offers, s.name, s.tenant_id, count(*) OVER() AS total
		FROM store_cards s WHERE s.public
		), page AS (
		SELECT * FROM filtered ORDER BY offers DESC, name, tenant_id LIMIT $1 OFFSET $2
		)
		SELECT COALESCE((SELECT jsonb_agg(card ORDER BY offers DESC, name, tenant_id) FROM page), '[]'::jsonb),
		COALESCE((SELECT total FROM filtered LIMIT 1), 0)`
	// Курсор и изменения читаются в одном снимке SQL. Ветка 304 не
	// обращается к записям и не сериализует тело.
	shopDirectory = `
		SELECT revision, CASE WHEN revision <> $1 THEN (
			SELECT jsonb_build_object(
				'items', COALESCE(jsonb_agg(card) FILTER (WHERE card IS NOT NULL), '[]'::jsonb),
				'removed', COALESCE(jsonb_agg(tenant_id) FILTER (WHERE card IS NULL AND $1 > 0), '[]'::jsonb))
			FROM shop_directory_changes WHERE revision > $1)
		END FROM shop_directory_revision`
	listCategoryShopCards = `
		WITH filtered AS (
		SELECT s.card || jsonb_build_object(
		'offers', c.count, 'category', jsonb_build_object('id', c.category_id, 'name', c.name)) AS card,
		c.count AS offers, s.name, s.tenant_id, count(*) OVER() AS total
		FROM store_card_categories c JOIN store_cards s ON s.tenant_id = c.tenant_id
		WHERE c.category_id = $1 AND c.count > 0 AND s.public
		), page AS (
		SELECT * FROM filtered ORDER BY offers DESC, name, tenant_id LIMIT $2 OFFSET $3
		)
		SELECT COALESCE((SELECT jsonb_agg(card ORDER BY offers DESC, name, tenant_id) FROM page), '[]'::jsonb),
		COALESCE((SELECT total FROM filtered LIMIT 1), 0)`
	updateStoreProfile = `
		INSERT INTO store_cards (tenant_id, slug, name, offers, public, summary, header, card, profile_version)
		VALUES ($1, $5, $6, 0, false, $7, $2, $8, $4)
		ON CONFLICT (tenant_id) DO UPDATE
		SET header = store_cards.header || $2::jsonb,
		card = store_cards.card || $8::jsonb,
		summary = jsonb_set(store_cards.summary, '{hidden}', $3::jsonb),
		profile_version = $4
		WHERE store_cards.profile_version <= $4`
)

// StoreCards хранит сводку и категории атомарно в транзакции пересчёта.
type StoreCards struct {
	db *gorm.DB
}

// CacheVersion читается из той же базы, что и проекция: отказ Redis не скрывает обновления.
func (r *StoreCards) CacheVersion(ctx context.Context, tenants ...ids.TenantID) (int64, error) {
	var version int64
	query, args := getAllStoreCardCacheVersion, []any{}
	if tenants != nil {
		keys := make([]string, len(tenants))
		for i, tenant := range tenants {
			keys[i] = string(tenant)
		}
		query, args = getStoreCardCacheVersion, []any{keys}
	}
	err := rawSQL(ctx, r.db, query, args...).Row().Scan(&version)
	return version, err
}

// Lock сериализует пересчёты магазина до чтения источников в READ COMMITTED.
func (r *StoreCards) Lock(ctx context.Context, tenant ids.TenantID) error {
	_, err := execSQL(ctx, r.db, lockStoreSummary, string(tenant))
	return err
}

// Save сохраняет сводку вместе со счётчиками категорий.
func (r *StoreCards) Save(ctx context.Context, record ports.StoreRecord) error {
	summary, err := json.Marshal(record.Summary)
	if err != nil {
		return err
	}
	header, err := json.Marshal(record.Header)
	if err != nil {
		return err
	}
	card, err := json.Marshal(record.Card)
	if err != nil {
		return err
	}
	h := record.Header
	_, err = execSQL(
		ctx,
		r.db,
		saveStoreSummary,
		string(h.TenantID),
		h.Slug,
		h.Name,
		h.Offers,
		record.Summary.FromPrice,
		record.Public,
		string(summary),
		string(header),
		string(card),
		record.ProfileVersion,
		record.PagePublic,
		record.CacheSignature,
	)
	if err != nil {
		return fmt.Errorf("сводка магазина %s: %w", h.TenantID, err)
	}
	if _, err := execSQL(ctx, r.db, deleteStoreCategories, string(h.TenantID)); err != nil {
		return err
	}
	if len(record.Summary.CategoryCounts) == 0 {
		return nil
	}
	categories := make([]ports.StoreCategory, 0, len(record.Summary.CategoryCounts))
	for _, category := range record.Summary.CategoryCounts {
		categories = append(categories, category)
	}
	raw, err := json.Marshal(categories)
	if err != nil {
		return err
	}
	_, err = execSQL(ctx, r.db, insertStoreCategories, string(h.TenantID), string(raw))
	return err
}

// Get читает внутреннюю сводку магазина.
func (r *StoreCards) Get(ctx context.Context, tenant ids.TenantID) (ports.StoreRecord, error) {
	var result ports.StoreRecord
	var summary, header, card []byte
	err := rawSQL(ctx, r.db, getStoreSummary, string(tenant)).Row().Scan(
		&summary,
		&header,
		&card,
		&result.Public,
		&result.ProfileVersion,
		&result.PagePublic,
	)
	if notFound(err) {
		return result, participant.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(summary, &result.Summary); err != nil {
		return result, err
	}
	if err := json.Unmarshal(header, &result.Header); err != nil {
		return result, err
	}
	err = json.Unmarshal(card, &result.Card)
	return result, err
}

// Shop читает готовую публичную шапку.
func (r *StoreCards) Shop(ctx context.Context, slug string) (ports.ShopHeader, error) {
	var result ports.ShopHeader
	var raw []byte
	err := rawSQL(ctx, r.db, getShopHeader, slug).Row().Scan(&raw)
	if notFound(err) {
		return result, participant.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(raw, &result)
	return result, err
}

// List читает страницу и общее число результатов одним SQL.
func (r *StoreCards) List(ctx context.Context, category string, limit, offset int) (ports.ShopPage, error) {
	result := ports.ShopPage{Limit: limit, Offset: offset, Items: []ports.ShopCard{}}
	var raw []byte
	query, args := listShopCards, []any{limit, offset}
	if category != "" {
		query, args = listCategoryShopCards, []any{category, limit, offset}
	}
	if err := rawSQL(ctx, r.db, query, args...).Row().Scan(&raw, &result.Total); err != nil {
		return result, err
	}
	err := json.Unmarshal(raw, &result.Items)
	return result, err
}

// Directory читает ревизию и при изменении карточки одним запросом.
func (r *StoreCards) Directory(ctx context.Context, previous int64) (ports.ShopDirectory, error) {
	var result ports.ShopDirectory
	var raw []byte
	if err := rawSQL(ctx, r.db, shopDirectory, previous).Row().Scan(&result.Revision, &raw); err != nil {
		return result, err
	}
	if raw == nil {
		return result, nil
	}
	err := json.Unmarshal(raw, &result)
	return result, err
}

// UpdateProfile меняет только поля постмодерации, не затрагивая счётчики и категории.
// До первого пересчёта создаёт непубличную строку: её версия защищает решение
// оператора от уже начавшегося пересчёта со старым снимком участника.
func (r *StoreCards) UpdateProfile(ctx context.Context, p *participant.Participant) error {
	profile := p.PublicProfile()
	header, err := json.Marshal(map[string]any{"logo": profile.Logo})
	if err != nil {
		return err
	}
	card, err := json.Marshal(map[string]any{"logo": profile.Logo, "mark": profile.Mark})
	if err != nil {
		return err
	}
	hidden, err := json.Marshal(append([]participant.CardHidden{}, p.State().StoreCard.Hidden...))
	if err != nil {
		return err
	}
	empty, err := json.Marshal(ports.StoreSummary{
		Categories: []ports.StoreCategory{},
		Products:   []ports.ShopCardProduct{},
		Hidden:     append([]participant.CardHidden{}, p.State().StoreCard.Hidden...),
	})
	if err != nil {
		return err
	}
	_, err = execSQL(
		ctx,
		r.db,
		updateStoreProfile,
		string(p.TenantID()),
		string(header),
		string(hidden),
		p.Version(),
		p.Slug(),
		p.Name(),
		string(empty),
		string(card),
	)
	return err
}

// BatchPublic сохраняет порядок подписок и исключает скрытые страницы.
func (r *StoreCards) BatchPublic(ctx context.Context, tenants []ids.TenantID) ([]ports.ShopCard, error) {
	keys := make([]string, len(tenants))
	for i, tenant := range tenants {
		keys[i] = string(tenant)
	}
	rows, err := rawSQL(ctx, r.db, batchPublicCards, keys).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := []ports.ShopCard{}
	for rows.Next() {
		var raw []byte
		var card ports.ShopCard
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &card); err != nil {
			return nil, err
		}
		result = append(result, card)
	}
	return result, rows.Err()
}
