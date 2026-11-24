package postgres

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// selectProductBatch читает товары списка ключей по первичному ключу одним запросом.
	selectProductBatch = `SELECT ` + productColumns + `
		FROM unnest($1::text[], $2::text[]) AS k(tenant_id, product_id)
		JOIN catalog_products p ON p.tenant_id = k.tenant_id AND p.product_id = k.product_id`

	// saveProducts заводит новые товары и обновляет прежние условно по исходной версии одним
	// оператором — и для одного товара, и для пачки. Записано меньше строк — конфликт.
	saveProducts = `
		WITH rows AS (
			SELECT * FROM unnest(
				$1::text[], $2::text[], $3::bigint[], $4::boolean[], $5::text[], $6::bigint[], $7::bigint[],
				$8::integer[], $9::timestamptz[], $10::bigint[], $11::timestamptz[], $12::bigint[],
				$13::float8[], $14::bigint[], $15::bigint[]
			) AS r(tenant_id, product_id, revision, deleted, card, price, base_price, available, projected_at,
				version, updated_at, sales_count, average_rating, ratings_count, origin)
		), inserted AS (
			INSERT INTO catalog_products (
				tenant_id,
				product_id,
				revision,
				deleted,
				card,
				price,
				base_price,
				available,
				projected_at,
				version,
				updated_at,
				sales_count,
				average_rating,
				ratings_count
			)
			SELECT tenant_id, product_id, revision, deleted, card::jsonb, price, base_price, available,
				projected_at, version, updated_at, sales_count, average_rating, ratings_count
			FROM rows
			WHERE origin = 0
			ON CONFLICT DO NOTHING
			RETURNING 1
		), updated AS (
			UPDATE catalog_products p
			SET revision = r.revision,
				deleted = r.deleted,
				card = r.card::jsonb,
				price = r.price,
				base_price = r.base_price,
				available = r.available,
				projected_at = r.projected_at,
				version = r.version,
				updated_at = r.updated_at,
				sales_count = r.sales_count,
				average_rating = r.average_rating,
				ratings_count = r.ratings_count
			FROM rows r
			WHERE r.origin > 0 AND p.tenant_id = r.tenant_id AND p.product_id = r.product_id AND p.version = r.origin
			RETURNING 1
		)
		SELECT (SELECT count(*) FROM inserted) + (SELECT count(*) FROM updated)`
)

// productWrites — колонки записи товаров; одна строка на товар, порядок — по ключу.
type productWrites struct {
	tenants, products     []string
	revisions             []int64
	deleted               []bool
	cards                 []*string
	prices, bases         []*int64
	available             []int
	projected, updated    []time.Time
	versions, salesCounts []int64
	averageRatings        []float64
	ratingsCounts         []int64
	origins               []int64
}

// Save заводит товар или обновляет его условно по версии — пачкой из одного.
func (r *Products) Save(ctx context.Context, product *catalog.Product) error {
	return r.SaveMany(ctx, []*catalog.Product{product})
}

// SaveMany пишет товары одним оператором в порядке ключа: параллельные пачки и перепроверка
// таксономии берут строки в одном порядке. Конфликт любой строки — ErrConflict.
func (r *Products) SaveMany(ctx context.Context, products []*catalog.Product) error {
	if len(products) == 0 {
		return nil
	}
	sorted := slices.Clone(products)
	slices.SortFunc(sorted, func(a, b *catalog.Product) int {
		left, right := a.State(), b.State()
		return cmp.Or(cmp.Compare(left.TenantID, right.TenantID), cmp.Compare(left.ProductID, right.ProductID))
	})
	writes, err := productWritesOf(sorted)
	if err != nil {
		return err
	}
	var written int64
	if err := rawSQL(ctx, r.db, saveProducts, writes.args()...).Row().Scan(&written); err != nil {
		first := sorted[0].State()
		return fmt.Errorf("товар %s/%s: %w", first.TenantID, first.ProductID, err)
	}
	if written != int64(len(sorted)) {
		return catalog.ErrConflict
	}
	for _, product := range sorted {
		product.MarkSaved()
	}
	return nil
}

// GetMany читает товары списка одним запросом по первичному ключу.
func (r *Products) GetMany(ctx context.Context, keys []ids.OfferID) (map[ids.OfferID]*catalog.Product, error) {
	tenants, products := offerKeyColumns(keys)
	rows, err := rawSQL(ctx, r.db, selectProductBatch, tenants, products).Rows()
	if err != nil {
		return nil, fmt.Errorf("пачка товаров: %w", err)
	}
	defer func() { _ = rows.Close() }()
	found := make(map[ids.OfferID]*catalog.Product, len(keys))
	for rows.Next() {
		var row productRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, fmt.Errorf("пачка товаров: %w", err)
		}
		product, err := row.product()
		if err != nil {
			return nil, err
		}
		state := product.State()
		found[ids.OfferID{Tenant: state.TenantID, Product: state.ProductID}] = product
	}
	return found, rows.Err()
}

func productWritesOf(products []*catalog.Product) (productWrites, error) {
	var w productWrites
	for _, product := range products {
		s := product.State()
		card, price, base, err := productPayload(s)
		if err != nil {
			return productWrites{}, err
		}
		w.tenants = append(w.tenants, string(s.TenantID))
		w.products = append(w.products, string(s.ProductID))
		w.revisions = append(w.revisions, s.Revision)
		w.deleted = append(w.deleted, s.Deleted)
		w.cards = append(w.cards, card)
		w.prices = append(w.prices, price)
		w.bases = append(w.bases, base)
		w.available = append(w.available, s.Available)
		w.projected = append(w.projected, s.ProjectedAt)
		w.versions = append(w.versions, s.Version)
		w.updated = append(w.updated, s.UpdatedAt)
		w.salesCounts = append(w.salesCounts, s.SalesCount)
		w.averageRatings = append(w.averageRatings, s.AverageRating)
		w.ratingsCounts = append(w.ratingsCounts, s.RatingsCount)
		w.origins = append(w.origins, product.OriginVersion())
	}
	return w, nil
}

// productPayload — карточка и цены товара; у удалённого товара их нет.
func productPayload(s catalog.ProductState) (*string, *int64, *int64, error) {
	if s.Deleted {
		return nil, nil, nil, nil
	}
	encoded, err := encodeCard(s.Card)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("карточка %s/%s: %w", s.TenantID, s.ProductID, err)
	}
	card := string(encoded)
	if !s.HasPrice {
		return &card, nil, nil, nil
	}
	price, base := s.Price.Kopecks(), s.BasePrice.Kopecks()
	return &card, &price, &base, nil
}

func (w productWrites) args() []any {
	return []any{
		w.tenants,
		w.products,
		w.revisions,
		w.deleted,
		w.cards,
		w.prices,
		w.bases,
		w.available,
		w.projected,
		w.versions,
		w.updated,
		w.salesCounts,
		w.averageRatings,
		w.ratingsCounts,
		w.origins,
	}
}
