package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	selectOfferRebuild = `
		SELECT started_at, tenant_id, product_id, indexed, skipped, complete
		FROM offer_index_rebuild WHERE singleton AND generation = $1`
	beginOfferRebuild = `
		INSERT INTO offer_index_rebuild (generation, started_at) VALUES ($1, $2)
		ON CONFLICT (singleton) DO UPDATE SET
			generation = EXCLUDED.generation,
			started_at = EXCLUDED.started_at,
			tenant_id = '',
			product_id = '',
			indexed = 0,
			skipped = 0,
			complete = false
		RETURNING started_at`
	saveOfferRebuild = `
		UPDATE offer_index_rebuild SET
			tenant_id = $4,
			product_id = $5,
			indexed = $6,
			skipped = $7,
			complete = $8
		WHERE singleton AND generation = $1 AND (tenant_id, product_id) = ($2, $3) AND NOT complete`

	// Курсор и актуальная версия читаются одним снимком. Версия складывается
	// из тех же ревизий участника и реестра, что входят в версию документа.
	selectTenantIndexProgress = `
		SELECT COALESCE(w.revision, 0), COALESCE(w.target, 0),
			COALESCE(w.product_id, ''), COALESCE(w.complete, true),
			COALESCE(pa.version, 0) + COALESCE(t.version, 0)
		FROM (SELECT $1::text AS id) tenant
		LEFT JOIN offer_index_tenant w ON w.tenant_id = tenant.id
		LEFT JOIN participants pa ON pa.tenant_id = tenant.id
		LEFT JOIN tenants t ON t.tenant_id = tenant.id`
	saveTenantIndexProgress = `
		INSERT INTO offer_index_tenant AS work (tenant_id, revision, target, product_id, complete)
		VALUES ($1, $2::bigint + 1, $3, $4, $5)
		ON CONFLICT (tenant_id) DO UPDATE SET
			revision = EXCLUDED.revision,
			target = EXCLUDED.target,
			product_id = EXCLUDED.product_id,
			complete = EXCLUDED.complete
		WHERE work.revision = $2`
)

type offerIndexWork struct {
	db *gorm.DB
}

func (s *scope) OfferIndexWork() ports.OfferIndexWork {
	return offerIndexWork{db: s.db}
}

func (r offerIndexWork) BeginRebuild(
	ctx context.Context,
	generation string,
	started time.Time,
) (ports.IndexRebuildProgress, error) {
	p := ports.IndexRebuildProgress{Generation: generation}
	err := rawSQL(ctx, r.db, selectOfferRebuild, generation).Row().Scan(
		&p.Started,
		&p.After.TenantID,
		&p.After.ProductID,
		&p.Indexed,
		&p.Skipped,
		&p.Complete,
	)
	if errors.Is(err, sql.ErrNoRows) {
		err = rawSQL(ctx, r.db, beginOfferRebuild, generation, started).Row().Scan(&p.Started)
	}
	return p, err
}

func (r offerIndexWork) SaveRebuild(ctx context.Context, from, to ports.IndexRebuildProgress) error {
	count, err := execSQL(
		ctx,
		r.db,
		saveOfferRebuild,
		from.Generation,
		string(from.After.TenantID),
		string(from.After.ProductID),
		string(to.After.TenantID),
		string(to.After.ProductID),
		to.Indexed,
		to.Skipped,
		to.Complete,
	)
	return indexProgressResult(count, err)
}

func (r offerIndexWork) TenantProgress(
	ctx context.Context,
	tenant ids.TenantID,
) (ports.TenantIndexProgress, int64, error) {
	var p ports.TenantIndexProgress
	var version int64
	err := rawSQL(ctx, r.db, selectTenantIndexProgress, string(tenant)).Row().Scan(
		&p.Revision,
		&p.Target,
		&p.After,
		&p.Complete,
		&version,
	)
	return p, version, err
}

func (r offerIndexWork) SaveTenant(ctx context.Context, tenant ids.TenantID, p ports.TenantIndexProgress) error {
	count, err := execSQL(
		ctx,
		r.db,
		saveTenantIndexProgress,
		string(tenant),
		p.Revision,
		p.Target,
		string(p.After),
		p.Complete,
	)
	return indexProgressResult(count, err)
}

func indexProgressResult(count int64, err error) error {
	if err == nil && count != 1 {
		return ports.ErrOfferIndexProgressConflict
	}
	return err
}
