package postgres

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	selectTaxonomy        = `SELECT definition FROM taxonomy_head WHERE singleton FOR SHARE`
	readTaxonomyVersion   = `SELECT version FROM taxonomy_head WHERE singleton FOR SHARE`
	selectTaxonomyVersion = `SELECT definition FROM taxonomy_versions WHERE version = $1`
	lockTaxonomy          = `SELECT version FROM taxonomy_head WHERE singleton FOR UPDATE`
	updateTaxonomy        = `UPDATE taxonomy_head
		SET version = $1,
			definition = $2
		WHERE singleton AND version = $3`
	insertTaxonomy = `INSERT INTO taxonomy_versions (version, definition) VALUES ($1, $2)`
	pageProducts   = `SELECT ` + productColumns + ` FROM catalog_products p
		WHERE (tenant_id, product_id) > ($1, $2) ORDER BY tenant_id, product_id LIMIT $3`
)

// Taxonomy — версии схем в PostgreSQL, блокировка головы согласует приём и загрузку.
type Taxonomy struct {
	db       *gorm.DB
	snapshot snapshotReader[taxonomy.Definition]
}

func newTaxonomy(
	db *gorm.DB,
	shared *snapshot[taxonomy.Definition],
	afterCommit func(func()),
) *Taxonomy {
	return &Taxonomy{
		db: db,
		snapshot: snapshotReader[taxonomy.Definition]{
			shared:      shared,
			afterCommit: afterCommit,
		},
	}
}

// Current читает голову с разделяемой блокировкой до фиксации транзакции.
func (r *Taxonomy) Current(ctx context.Context) (taxonomy.Definition, error) {
	var version int64
	if err := rawSQL(ctx, r.db, readTaxonomyVersion).Row().Scan(&version); err != nil {
		return taxonomy.Definition{}, err
	}

	return r.snapshot.current(version, func() (taxonomy.Definition, error) { return r.load(ctx) })
}

func (r *Taxonomy) load(ctx context.Context) (taxonomy.Definition, error) {
	var raw []byte
	if err := rawSQL(ctx, r.db, selectTaxonomy).Row().Scan(&raw); err != nil {
		return taxonomy.Definition{}, err
	}
	return decodeTaxonomySnapshot(raw)
}

// Version читает неизменяемое доказательство прежней привязки, не восстанавливая снимок целиком.
func (r *Taxonomy) Version(ctx context.Context, version int64) (taxonomy.Definition, error) {
	var raw []byte
	err := rawSQL(ctx, r.db, selectTaxonomyVersion, version).Row().Scan(&raw)
	if notFound(err) {
		return taxonomy.Definition{}, taxonomy.ErrInvalid.WithMessage("неизвестная версия таксономии")
	}
	if err != nil {
		return taxonomy.Definition{}, err
	}
	var d taxonomy.Definition
	err = json.Unmarshal(raw, &d)
	return d, err
}

// Старые опубликованные снимки хранят развёрнутые hiddenFacets. Преобразуем их
// при чтении версии, включая исходный снимок отката.
func decodeTaxonomySnapshot(raw []byte) (taxonomy.Definition, error) {
	var d taxonomy.Definition
	if err := json.Unmarshal(raw, &d); err != nil || d.Version == 0 || d.FacetKeys != nil {
		return d, err
	}
	var legacy struct {
		Schemas map[string]struct {
			HiddenFacets []string `json:"hiddenFacets"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return taxonomy.Definition{}, err
	}
	for name, previous := range legacy.Schemas {
		if len(previous.HiddenFacets) == 0 {
			continue
		}
		schema := d.Schemas[name]
		schema.FacetOverrides = make(map[string]taxonomy.FacetOverride, len(previous.HiddenFacets))
		for _, code := range previous.HiddenFacets {
			schema.FacetOverrides[code] = taxonomy.FacetOff
		}
		d.Schemas[name] = schema
	}
	return d.CompileFacets(false)
}

// Lock блокирует загрузчиков и приём карточек на время замены схемы.
func (r *Taxonomy) Lock(ctx context.Context) error {
	var version int64
	return rawSQL(ctx, r.db, lockTaxonomy).Row().Scan(&version)
}

// Save условно заменяет голову и сохраняет неизменяемую версию.
func (r *Taxonomy) Save(ctx context.Context, d taxonomy.Definition, expected int64) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	tag, err := execSQL(ctx, r.db, updateTaxonomy, d.Version, string(raw), expected)
	if err != nil {
		return err
	}
	if tag != 1 {
		return taxonomy.ErrConflict
	}
	_, err = execSQL(ctx, r.db, insertTaxonomy, d.Version, string(raw))
	return err
}

// Page читает все проекции, включая магазины без участия, в порядке ключа.
func (r *Products) Page(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
	limit int,
) ([]*catalog.Product, error) {
	rows, err := rawSQL(ctx, r.db, pageProducts, tenant, product, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	out := []*catalog.Product{}
	for rows.Next() {
		var row productRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, err
		}
		p, err := row.product()
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
