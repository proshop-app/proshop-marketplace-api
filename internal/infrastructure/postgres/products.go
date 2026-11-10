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
		p.promo_price,
		p.available,
		p.projected_at,
		p.version,
		p.updated_at,
		p.price_profiles,
		p.sales_count,
		p.average_rating,
		p.ratings_count`

	selectProduct = `
		SELECT ` + productColumns + `
		FROM catalog_products p
		WHERE p.tenant_id = $1 AND p.product_id = $2`

	insertProduct = `
		INSERT INTO catalog_products (
			tenant_id,
			product_id,
			revision,
			deleted,
			card,
			price,
			promo_price,
			available,
			projected_at,
			version,
			updated_at,
			price_profiles,
			sales_count,
			average_rating,
			ratings_count
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`

	updateProduct = `
		UPDATE catalog_products
		SET revision = $3,
			deleted = $4,
			card = $5,
			price = $6,
			promo_price = $7,
			available = $8,
			projected_at = $9,
			version = $10,
			updated_at = $11,
			price_profiles = $12,
			sales_count = $13,
			average_rating = $14,
			ratings_count = $15
		WHERE tenant_id = $1 AND product_id = $2 AND version = $16`
)

// Products — проекция товаров в PostgreSQL.
type Products struct {
	db *gorm.DB
}

// cardJSON — карточка товара в колонке jsonb.
type cardJSON struct {
	Variants       *catalog.Variants    `json:"variants,omitempty"`
	ModelKey       string               `json:"modelKey,omitempty"`
	ModelKeySource string               `json:"modelKeySource,omitempty"`
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
	OnlyVariant    bool                 `json:"onlyVariant,omitempty"`
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
	priceProfiles       []byte
	price, promo        *int64
	available           int
	projectedAt         time.Time
	version             int64
	updatedAt           time.Time
}

// Get — товар проекции.
func (r *Products) Get(ctx context.Context, tenantID ids.TenantID, productID ids.ProductID) (*catalog.Product, error) {
	var row productRow
	err := rawSQL(ctx, r.db, selectProduct, string(tenantID), string(productID)).Row().Scan(row.targets()...)
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

	var card, price, promo any

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

	profiles, err := json.Marshal(s.PriceProfiles)
	if err != nil {
		return err
	}
	args := []any{
		string(s.TenantID),
		string(s.ProductID),
		s.Revision,
		s.Deleted,
		card,
		price,
		promo,
		s.Available,
		s.ProjectedAt,
		s.Version,
		s.UpdatedAt,
		string(profiles),
		s.SalesCount,
		s.AverageRating,
		s.RatingsCount,
	}

	if product.OriginVersion() == 0 {
		_, err := execSQL(ctx, r.db, insertProduct, args...)
		conflict := isUnique(err, "catalog_products_pkey")

		return saved(err, conflict, product.MarkSaved, "товар", s.TenantID, s.ProductID)
	}

	affected, err := execSQL(ctx, r.db, updateProduct, append(args, product.OriginVersion())...)
	conflict := err == nil && affected == 0

	return saved(err, conflict, product.MarkSaved, "товар", s.TenantID, s.ProductID)
}

func (r *productRow) targets() []any {
	return []any{
		&r.tenantID,
		&r.productID,
		&r.revision,
		&r.deleted,
		&r.card,
		&r.price,
		&r.promo,
		&r.available,
		&r.projectedAt,
		&r.version,
		&r.updatedAt,
		&r.priceProfiles,
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

	if err := json.Unmarshal(r.priceProfiles, &state.PriceProfiles); err != nil {
		return nil, fmt.Errorf("ценовые профили %s/%s: %w", r.tenantID, r.productID, err)
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

func encodeCard(card catalog.Card) ([]byte, error) {
	doc := cardJSON{
		Marketplace:    card.Marketplace,
		Variants:       card.Variants,
		ModelKey:       card.ModelKey,
		ModelKeySource: card.ModelKeySource,
		Publication:    card.Publication,
		Name:           card.Name,
		Description:    card.Description,
		Image:          card.Image,
		Assets:         card.Assets,
		URL:            card.URL,
		SKU:            card.SKU,
		VariantsID:     card.VariantsID,
		Visible:        card.Visible,
		OnlyVariant:    card.OnlyVariant,
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
		Publication:    doc.Publication,
		Name:           doc.Name,
		Description:    doc.Description,
		Image:          doc.Image,
		Assets:         doc.Assets,
		URL:            doc.URL,
		SKU:            doc.SKU,
		VariantsID:     doc.VariantsID,
		Visible:        doc.Visible,
		OnlyVariant:    doc.OnlyVariant,
	}

	for _, category := range doc.Categories {
		card.Categories = append(card.Categories, catalog.Category(category))
	}

	return card, nil
}
