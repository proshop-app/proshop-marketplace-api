package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	productColumns = `
		p.tenant_id,
		p.product_id,
		p.revision,
		p.deleted,
		p.card,
		p.price,
		p.base_price,
		p.available,
		p.projected_at,
		p.version,
		p.updated_at,
		p.sales_count,
		p.average_rating,
		p.ratings_count`

	selectProduct = `
		SELECT ` + productColumns + `
		FROM catalog_products p
		WHERE p.tenant_id = $1 AND p.product_id = $2`
	selectProductForUpdate = selectProduct + ` FOR UPDATE`
)

// Products — проекция товаров в PostgreSQL.
type Products struct {
	db *gorm.DB
}

// cardJSON — карточка товара в колонке jsonb.
type cardJSON struct {
	TileKey        string               `json:"tileKey,omitempty"`
	Variants       *catalog.Variants    `json:"variants,omitempty"`
	ModelKey       string               `json:"modelKey,omitempty"`
	ModelKeySource string               `json:"modelKeySource,omitempty"`
	VariantKey     string               `json:"variantKey,omitempty"`
	Marketplace    *taxonomy.Card       `json:"marketplace,omitempty"`
	Publication    taxonomy.Publication `json:"publication"`
	Name           string               `json:"name"`
	Description    string               `json:"description,omitempty"`
	Image          string               `json:"image,omitempty"`
	Assets         []string             `json:"assets"`
	URL            string               `json:"url,omitempty"`
	SKU            string               `json:"sku,omitempty"`
	Categories     []categoryJSON       `json:"categories,omitempty"`
	VariantsID     string               `json:"variantsId,omitempty"`
	Visible        bool                 `json:"visible"`
}

type categoryJSON struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
}

// productRow — строка товара: цены пусты у удалённого товара.
type productRow struct {
	tenantID, productID string
	revision            int64
	salesCount          int64
	averageRating       float64
	ratingsCount        int64
	deleted             bool
	card                []byte
	price, base         *int64
	available           int
	projectedAt         time.Time
	version             int64
	updatedAt           time.Time
}

// Get — товар проекции.
func (r *Products) Get(ctx context.Context, tenantID ids.TenantID, productID ids.ProductID) (*catalog.Product, error) {
	return r.get(ctx, selectProduct, tenantID, productID)
}

// GetForUpdate не даёт событию изменить источник между проверкой и переносом решения.
func (r *Products) GetForUpdate(
	ctx context.Context,
	tenantID ids.TenantID,
	productID ids.ProductID,
) (*catalog.Product, error) {
	return r.get(ctx, selectProductForUpdate, tenantID, productID)
}

func (r *Products) get(
	ctx context.Context,
	query string,
	tenantID ids.TenantID,
	productID ids.ProductID,
) (*catalog.Product, error) {
	var row productRow
	err := rawSQL(ctx, r.db, query, string(tenantID), string(productID)).Row().Scan(row.targets()...)
	if notFound(err) {
		return nil, catalog.ErrProductNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("товар %s/%s: %w", tenantID, productID, err)
	}

	return row.product()
}

func (r *productRow) targets() []any {
	return r.targetsWithTimes(&r.projectedAt, &r.updatedAt)
}

func (r *productRow) targetsWithTimes(projected, updated any) []any {
	return []any{
		&r.tenantID,
		&r.productID,
		&r.revision,
		&r.deleted,
		&r.card,
		&r.price,
		&r.base,
		&r.available,
		projected,
		&r.version,
		updated,
		&r.salesCount,
		&r.averageRating,
		&r.ratingsCount,
	}
}

func (r *productRow) product() (*catalog.Product, error) {
	card, err := decodeCard(r.card)
	if err != nil {
		return nil, fmt.Errorf("карточка %s/%s: %w", r.tenantID, r.productID, err)
	}
	state := catalog.ProductState{
		TenantID:      ids.TenantID(r.tenantID),
		ProductID:     ids.ProductID(r.productID),
		Revision:      r.revision,
		SalesCount:    r.salesCount,
		AverageRating: r.averageRating,
		RatingsCount:  r.ratingsCount,
		Deleted:       r.deleted,
		Card:          card,
		Available:     r.available,
		ProjectedAt:   r.projectedAt,
		Version:       r.version,
		UpdatedAt:     r.updatedAt,
	}

	if r.price != nil {
		state.HasPrice = true
		state.Price = money.FromKopecks(*r.price)
	}

	if r.base != nil {
		state.BasePrice = money.FromKopecks(*r.base)
	}

	return catalog.RestoreProduct(state), nil
}

func encodeCard(card catalog.Card) ([]byte, error) {
	doc := cardJSON{
		Marketplace:    card.Marketplace,
		Variants:       card.Variants,
		ModelKey:       card.ModelKey,
		ModelKeySource: card.ModelKeySource,
		VariantKey:     card.VariantKey,
		Publication:    card.Publication,
		Name:           card.Name,
		Description:    card.Description,
		Image:          card.Image,
		Assets:         card.Assets,
		URL:            card.URL,
		SKU:            card.SKU,
		VariantsID:     card.VariantsID,
		TileKey:        card.TileKey,
		Visible:        card.Visible,
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
		Marketplace:    doc.Marketplace,
		Variants:       doc.Variants,
		ModelKey:       doc.ModelKey,
		ModelKeySource: doc.ModelKeySource,
		VariantKey:     doc.VariantKey,
		Publication:    doc.Publication,
		Name:           doc.Name,
		Description:    doc.Description,
		Image:          doc.Image,
		Assets:         doc.Assets,
		URL:            doc.URL,
		SKU:            doc.SKU,
		VariantsID:     doc.VariantsID,
		TileKey:        doc.TileKey,
		Visible:        doc.Visible,
	}

	for _, category := range doc.Categories {
		card.Categories = append(card.Categories, catalog.Category(category))
	}

	return card, nil
}
