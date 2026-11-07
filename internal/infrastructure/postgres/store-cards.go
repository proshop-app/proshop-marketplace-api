package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	batchPublicCards = `SELECT card FROM store_cards WHERE tenant_id = ANY($1) AND page_public
		ORDER BY array_position($1::text[], tenant_id)`
	lockStoreSummary = `SELECT pg_advisory_xact_lock(hashtextextended($1, 38))`
	saveStoreSummary = `
		INSERT INTO store_cards (
		 tenant_id, slug, name, offers, from_price, public, summary, header, card, profile_version, page_public
		)
		VALUES (
		$1,
		$2,
		$3,
		$4,
		$5,
		$6,
		$7,
		$8,
		$9,
		$10,
		$11
		)
		ON CONFLICT (tenant_id) DO UPDATE
		SET
		slug = EXCLUDED.slug,
		name = EXCLUDED.name,
		offers = EXCLUDED.offers,
		from_price = EXCLUDED.from_price,
		public = EXCLUDED.public,
		page_public = EXCLUDED.page_public,
		summary = CASE WHEN store_cards.profile_version > EXCLUDED.profile_version
		THEN jsonb_set(EXCLUDED.summary, '{hidden}', store_cards.summary->'hidden') ELSE EXCLUDED.summary END,
		header = CASE WHEN store_cards.profile_version > EXCLUDED.profile_version
		THEN EXCLUDED.header || jsonb_build_object(
		 'logo', store_cards.header->'logo')
		ELSE EXCLUDED.header END,
		card = CASE WHEN store_cards.profile_version > EXCLUDED.profile_version
		THEN EXCLUDED.card || jsonb_build_object('logo', store_cards.card->'logo')
		ELSE EXCLUDED.card END,
		profile_version = GREATEST(store_cards.profile_version, EXCLUDED.profile_version)`
	deleteStoreCategories = `DELETE FROM store_card_categories WHERE tenant_id = $1`
	insertStoreCategory   = `
		INSERT INTO store_card_categories (category_id, tenant_id, name, count) VALUES (
		$1,
		$2,
		$3,
		$4
		)`
	getStoreSummary = `SELECT summary, header, card, public, profile_version, page_public
		FROM store_cards WHERE tenant_id = $1`
	getShopHeader = `SELECT header || jsonb_build_object('showcase', COALESCE(card->'showcase', '[]'::jsonb))
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
		VALUES ($1, $5, $6, 0, false, $7, $2, $2, $4)
		ON CONFLICT (tenant_id) DO UPDATE
		SET header = store_cards.header || $2::jsonb,
		card = store_cards.card || $2::jsonb,
		summary = jsonb_set(store_cards.summary, '{hidden}', $3::jsonb),
		profile_version = $4
		WHERE store_cards.profile_version <= $4`
)

// StoreCards хранит сводку и категории атомарно в транзакции пересчёта.
type StoreCards struct{ q querier }

// Lock сериализует пересчёты магазина до чтения источников в READ COMMITTED.
func (r *StoreCards) Lock(ctx context.Context, tenant ids.TenantID) error {
	_, err := r.q.Exec(ctx, lockStoreSummary, string(tenant))
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
	_, err = r.q.Exec(
		ctx,
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
	)
	if err != nil {
		return fmt.Errorf("сводка магазина %s: %w", h.TenantID, err)
	}
	if _, err := r.q.Exec(ctx, deleteStoreCategories, string(h.TenantID)); err != nil {
		return err
	}
	for _, category := range record.Summary.CategoryCounts {
		if _, err := r.q.Exec(
			ctx,
			insertStoreCategory,
			category.ID,
			string(h.TenantID),
			category.Name,
			category.Count,
		); err != nil {
			return err
		}
	}
	return nil
}

// Get читает внутреннюю сводку магазина.
func (r *StoreCards) Get(ctx context.Context, tenant ids.TenantID) (ports.StoreRecord, error) {
	var result ports.StoreRecord
	var summary, header, card []byte
	err := r.q.QueryRow(ctx, getStoreSummary, string(tenant)).Scan(
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
	err := r.q.QueryRow(ctx, getShopHeader, slug).Scan(&raw)
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
	if err := r.q.QueryRow(ctx, query, args...).Scan(&raw, &result.Total); err != nil {
		return result, err
	}
	err := json.Unmarshal(raw, &result.Items)
	return result, err
}

// UpdateProfile меняет только поля постмодерации, не затрагивая счётчики и категории.
// До первого пересчёта создаёт непубличную строку: её версия защищает решение
// оператора от уже начавшегося пересчёта со старым снимком участника.
func (r *StoreCards) UpdateProfile(ctx context.Context, p *participant.Participant) error {
	profile := p.PublicProfile()
	patch, err := json.Marshal(map[string]any{"logo": profile.Logo})
	if err != nil {
		return err
	}
	hidden, err := json.Marshal(append([]participant.CardHidden{}, p.State().StoreCard.Hidden...))
	if err != nil {
		return err
	}
	empty, err := json.Marshal(ports.StoreSummary{
		Categories: []ports.StoreCategory{},
		Showcase:   []ports.StoreProduct{},
		Hidden:     append([]participant.CardHidden{}, p.State().StoreCard.Hidden...),
	})
	if err != nil {
		return err
	}
	_, err = r.q.Exec(
		ctx,
		updateStoreProfile,
		string(p.TenantID()),
		string(patch),
		string(hidden),
		p.Version(),
		p.Slug(),
		p.Name(),
		string(empty),
	)
	return err
}

// BatchPublic сохраняет порядок подписок и исключает скрытые страницы.
func (r *StoreCards) BatchPublic(ctx context.Context, tenants []ids.TenantID) ([]ports.ShopCard, error) {
	keys := make([]string, len(tenants))
	for i, tenant := range tenants {
		keys[i] = string(tenant)
	}
	rows, err := r.q.Query(ctx, batchPublicCards, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
