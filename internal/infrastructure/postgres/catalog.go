package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// cardJSON — карточка товара в колонке jsonb.
type cardJSON struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Image       string         `json:"image,omitempty"`
	URL         string         `json:"url,omitempty"`
	SKU         string         `json:"sku,omitempty"`
	Categories  []categoryJSON `json:"categories,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	VariantsID  string         `json:"variantsId,omitempty"`
	Visible     bool           `json:"visible"`
	OnlyVariant bool           `json:"onlyVariant,omitempty"`
}

type categoryJSON struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
}

func encodeCard(card catalog.Card) ([]byte, error) {
	doc := cardJSON{
		Name: card.Name, Description: card.Description, Image: card.Image, URL: card.URL, SKU: card.SKU,
		Tags: card.Tags, VariantsID: card.VariantsID, Visible: card.Visible, OnlyVariant: card.OnlyVariant,
	}
	for _, category := range card.Categories {
		doc.Categories = append(doc.Categories, categoryJSON(category))
	}

	return json.Marshal(doc)
}

func decodeCard(raw []byte) (catalog.Card, error) {
	if len(raw) == 0 {
		return catalog.Card{}, nil
	}

	var doc cardJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return catalog.Card{}, err
	}

	card := catalog.Card{
		Name: doc.Name, Description: doc.Description, Image: doc.Image, URL: doc.URL, SKU: doc.SKU,
		Tags: doc.Tags, VariantsID: doc.VariantsID, Visible: doc.Visible, OnlyVariant: doc.OnlyVariant,
	}
	for _, category := range doc.Categories {
		card.Categories = append(card.Categories, catalog.Category(category))
	}

	return card, nil
}

const productColumns = `p.tenant_id, p.product_id, p.revision, p.deleted, p.card, p.price, p.promo_price,
	p.available, p.projected_at, p.version, p.updated_at`

type productRow struct {
	tenantID, productID string
	revision            int64
	deleted             bool
	card                []byte
	price, promo        *int64
	available           int
	projectedAt         time.Time
	version             int64
	updatedAt           time.Time
}

func (r *productRow) targets() []any {
	return []any{&r.tenantID, &r.productID, &r.revision, &r.deleted, &r.card, &r.price, &r.promo,
		&r.available, &r.projectedAt, &r.version, &r.updatedAt}
}

func (r *productRow) product() (*catalog.Product, error) {
	card, err := decodeCard(r.card)
	if err != nil {
		return nil, fmt.Errorf("карточка %s/%s: %w", r.tenantID, r.productID, err)
	}

	state := catalog.ProductState{
		TenantID: ids.TenantID(r.tenantID), ProductID: ids.ProductID(r.productID), Revision: r.revision,
		Deleted: r.deleted, Card: card, Available: r.available, ProjectedAt: r.projectedAt,
		Version: r.version, UpdatedAt: r.updatedAt,
	}
	if r.price != nil {
		state.Price = money.FromKopecks(*r.price)
	}
	if r.promo != nil {
		promo := money.FromKopecks(*r.promo)
		state.PromoPrice = &promo
	}

	return catalog.RestoreProduct(state), nil
}

// Products — проекция товаров в PostgreSQL.
type Products struct {
	q querier
}

// Get — товар проекции.
func (r *Products) Get(ctx context.Context, tenantID ids.TenantID, productID ids.ProductID) (*catalog.Product, error) {
	var row productRow

	err := r.q.QueryRow(ctx, `SELECT `+productColumns+` FROM catalog_products p
		WHERE p.tenant_id = $1 AND p.product_id = $2`, string(tenantID), string(productID)).Scan(row.targets()...)
	if notFound(err) {
		return nil, catalog.ErrProductNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("товар %s/%s: %w", tenantID, productID, err)
	}

	return row.product()
}

// Save заводит товар или обновляет его условно по версии.
func (r *Products) Save(ctx context.Context, product *catalog.Product) error {
	s := product.State()

	var (
		card  any
		price any
		promo any
	)

	if !s.Deleted {
		encoded, err := encodeCard(s.Card)
		if err != nil {
			return fmt.Errorf("карточка %s/%s: %w", s.TenantID, s.ProductID, err)
		}

		card = string(encoded)
		price = s.Price.Kopecks()
	}
	if s.PromoPrice != nil {
		promo = s.PromoPrice.Kopecks()
	}

	args := []any{string(s.TenantID), string(s.ProductID), s.Revision, s.Deleted, card, price, promo,
		s.Available, s.ProjectedAt, s.Version, s.UpdatedAt}

	if product.OriginVersion() == 0 {
		_, err := r.q.Exec(ctx, `INSERT INTO catalog_products
			(tenant_id, product_id, revision, deleted, card, price, promo_price, available, projected_at, version, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, args...)

		return saved(err, isUnique(err, "catalog_products_pkey"), product.MarkSaved, "товар", s.TenantID, s.ProductID)
	}

	tag, err := r.q.Exec(ctx, `UPDATE catalog_products
		SET revision = $3, deleted = $4, card = $5, price = $6, promo_price = $7, available = $8,
			projected_at = $9, version = $10, updated_at = $11
		WHERE tenant_id = $1 AND product_id = $2 AND version = $12`, append(args, product.OriginVersion())...)

	return saved(err, err == nil && tag.RowsAffected() == 0, product.MarkSaved, "товар", s.TenantID, s.ProductID)
}

func saved(err error, conflict bool, mark func(), subject string, tenantID ids.TenantID, id any) error {
	if conflict {
		return catalog.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("%s %s/%v: %w", subject, tenantID, id, err)
	}

	mark()

	return nil
}

const signalsColumns = `o.tenant_id, o.product_id, o.available, o.stock_at, o.price, o.price_at, o.version, o.updated_at`

type signalsRow struct {
	tenantID, productID *string
	available           *int
	stockAt             *time.Time
	price               *int64
	priceAt             *time.Time
	version             *int64
	updatedAt           *time.Time
}

func (r *signalsRow) targets() []any {
	return []any{&r.tenantID, &r.productID, &r.available, &r.stockAt, &r.price, &r.priceAt, &r.version, &r.updatedAt}
}

func (r *signalsRow) signals() *catalog.Signals {
	if r.tenantID == nil {
		return nil
	}

	state := catalog.SignalsState{
		TenantID: ids.TenantID(*r.tenantID), ProductID: ids.ProductID(*r.productID),
		Version: *r.version, UpdatedAt: *r.updatedAt,
	}
	if r.available != nil {
		state.HasStock, state.Available, state.StockAt = true, *r.available, *r.stockAt
	}
	if r.price != nil {
		state.HasPrice, state.Price, state.PriceAt = true, money.FromKopecks(*r.price), *r.priceAt
	}

	return catalog.RestoreSignals(state)
}

// Signals — проекция остатков и цен в PostgreSQL.
type Signals struct {
	q querier
}

// Get — сигналы товара.
func (r *Signals) Get(ctx context.Context, tenantID ids.TenantID, productID ids.ProductID) (*catalog.Signals, error) {
	var row signalsRow

	err := r.q.QueryRow(ctx, `SELECT `+signalsColumns+` FROM offers o
		WHERE o.tenant_id = $1 AND o.product_id = $2`, string(tenantID), string(productID)).Scan(row.targets()...)
	if notFound(err) {
		return nil, catalog.ErrSignalsNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("сигналы %s/%s: %w", tenantID, productID, err)
	}

	return row.signals(), nil
}

// Save заводит сигналы или обновляет их условно по версии.
func (r *Signals) Save(ctx context.Context, signals *catalog.Signals) error {
	s := signals.State()

	var available, stockAt, price, priceAt any
	if s.HasStock {
		available, stockAt = s.Available, nullTime(s.StockAt)
	}
	if s.HasPrice {
		price, priceAt = s.Price.Kopecks(), nullTime(s.PriceAt)
	}

	args := []any{string(s.TenantID), string(s.ProductID), available, stockAt, price, priceAt, s.Version, s.UpdatedAt}

	if signals.OriginVersion() == 0 {
		_, err := r.q.Exec(ctx, `INSERT INTO offers
			(tenant_id, product_id, available, stock_at, price, price_at, version, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, args...)

		return saved(err, isUnique(err, "offers_pkey"), signals.MarkSaved, "сигналы", s.TenantID, s.ProductID)
	}

	tag, err := r.q.Exec(ctx, `UPDATE offers
		SET available = $3, stock_at = $4, price = $5, price_at = $6, version = $7, updated_at = $8
		WHERE tenant_id = $1 AND product_id = $2 AND version = $9`, append(args, signals.OriginVersion())...)

	return saved(err, err == nil && tag.RowsAffected() == 0, signals.MarkSaved, "сигналы", s.TenantID, s.ProductID)
}

// Tags — проекция тегов в PostgreSQL.
type Tags struct {
	q querier
}

// Get — тег.
func (r *Tags) Get(ctx context.Context, tenantID ids.TenantID, tagID ids.TagID) (*catalog.Tag, error) {
	var (
		state       catalog.TagState
		tenant, tag string
	)

	err := r.q.QueryRow(ctx, `SELECT tenant_id, tag_id, revision, deleted, value, version, updated_at
		FROM catalog_tags WHERE tenant_id = $1 AND tag_id = $2`, string(tenantID), string(tagID)).
		Scan(&tenant, &tag, &state.Revision, &state.Deleted, &state.Value, &state.Version, &state.UpdatedAt)
	if notFound(err) {
		return nil, catalog.ErrTagNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("тег %s/%s: %w", tenantID, tagID, err)
	}

	state.TenantID, state.TagID = ids.TenantID(tenant), ids.TagID(tag)

	return catalog.RestoreTag(state), nil
}

// Save заводит тег или обновляет его условно по версии.
func (r *Tags) Save(ctx context.Context, tag *catalog.Tag) error {
	s := tag.State()
	args := []any{string(s.TenantID), string(s.TagID), s.Revision, s.Deleted, s.Value, s.Version, s.UpdatedAt}

	if tag.OriginVersion() == 0 {
		_, err := r.q.Exec(ctx, `INSERT INTO catalog_tags (tenant_id, tag_id, revision, deleted, value, version, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, args...)

		return saved(err, isUnique(err, "catalog_tags_pkey"), tag.MarkSaved, "тег", s.TenantID, s.TagID)
	}

	cmd, err := r.q.Exec(ctx, `UPDATE catalog_tags SET revision = $3, deleted = $4, value = $5, version = $6, updated_at = $7
		WHERE tenant_id = $1 AND tag_id = $2 AND version = $8`, append(args, tag.OriginVersion())...)

	return saved(err, err == nil && cmd.RowsAffected() == 0, tag.MarkSaved, "тег", s.TenantID, s.TagID)
}

// Labels — значения неудалённых тегов магазина.
func (r *Tags) Labels(ctx context.Context, tenantID ids.TenantID, tagIDs []string) (map[string]string, error) {
	rows, err := r.q.Query(ctx, `SELECT tag_id, value FROM catalog_tags
		WHERE tenant_id = $1 AND tag_id = ANY($2) AND NOT deleted`, string(tenantID), tagIDs)
	if err != nil {
		return nil, fmt.Errorf("подписи тегов %s: %w", tenantID, err)
	}
	defer rows.Close()

	labels := make(map[string]string, len(tagIDs))
	for rows.Next() {
		var id, value string
		if err := rows.Scan(&id, &value); err != nil {
			return nil, fmt.Errorf("подписи тегов %s: %w", tenantID, err)
		}

		labels[id] = value
	}

	return labels, rows.Err()
}
