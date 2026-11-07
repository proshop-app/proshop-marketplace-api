package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	selectTaxonomyVersion = `SELECT definition FROM taxonomy_versions WHERE version = $1`
	selectTaxonomy        = `SELECT definition FROM taxonomy_head WHERE singleton FOR SHARE`
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
type Taxonomy struct{ q querier }

// Current читает голову с разделяемой блокировкой до фиксации транзакции.
func (r *Taxonomy) Current(ctx context.Context) (taxonomy.Definition, error) {
	var raw []byte
	if err := r.q.QueryRow(ctx, selectTaxonomy).Scan(&raw); err != nil {
		return taxonomy.Definition{}, err
	}
	var d taxonomy.Definition
	err := json.Unmarshal(raw, &d)
	return d, err
}

// Lock блокирует загрузчиков и приём карточек на время замены схемы.
func (r *Taxonomy) Lock(ctx context.Context) error {
	var version int64
	return r.q.QueryRow(ctx, lockTaxonomy).Scan(&version)
}

// Save условно заменяет голову и сохраняет неизменяемую версию.
func (r *Taxonomy) Save(ctx context.Context, d taxonomy.Definition, expected int64) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	tag, err := r.q.Exec(ctx, updateTaxonomy, d.Version, string(raw), expected)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return taxonomy.ErrConflict
	}
	_, err = r.q.Exec(ctx, insertTaxonomy, d.Version, string(raw))
	return err
}

// Page читает все проекции, включая магазины без участия, в порядке ключа.
func (r *Products) Page(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
	limit int,
) ([]*catalog.Product, error) {
	rows, err := r.q.Query(ctx, pageProducts, tenant, product, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

// Version нужен для сравнения эффективной политики с последней проверкой товара.
func (r *Taxonomy) Version(ctx context.Context, version int64) (taxonomy.Definition, error) {
	if version == 0 {
		return taxonomy.Definition{}, nil
	}
	var raw []byte
	if err := r.q.QueryRow(ctx, selectTaxonomyVersion, version).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = taxonomy.ErrConflict
		}
		return taxonomy.Definition{}, fmt.Errorf("снимок таксономии %d: %w", version, err)
	}
	var d taxonomy.Definition
	err := json.Unmarshal(raw, &d)
	return d, err
}
