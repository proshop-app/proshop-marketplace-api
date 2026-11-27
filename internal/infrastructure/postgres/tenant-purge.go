package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	openTenants      = `SELECT * FROM marketplace_open_tenants($1::text[], true)`
	beginTenantPurge = `
		WITH locked AS MATERIALIZED (SELECT pg_advisory_xact_lock(hashtextextended($1, 83)))
		INSERT INTO marketplace_tenant_deletions (tenant_id) SELECT $1 FROM locked
		ON CONFLICT DO NOTHING`
	lockPurgeCursor = `SELECT step, product_id, index_done, complete
		FROM marketplace_tenant_deletions WHERE tenant_id = $1 FOR UPDATE`
	purgeIndexProducts = `SELECT to_jsonb(ARRAY(
    SELECT '~tile~' || key FROM (
      (SELECT p.tile_key AS key FROM catalog_products p
       WHERE p.tenant_id = d.tenant_id AND p.tile_key > cursor.key
       GROUP BY p.tile_key ORDER BY p.tile_key LIMIT $3)
      UNION
      (SELECT x.tile_key FROM catalog_tile_departures x
       WHERE x.tenant_id = d.tenant_id AND x.tile_key > cursor.key
       ORDER BY x.tile_key LIMIT $3)
    ) tiles ORDER BY key LIMIT $3
  ))
 FROM marketplace_tenant_deletions d
 CROSS JOIN LATERAL (SELECT CASE WHEN left(d.product_id, 6) = '~tile~'
   THEN substr(d.product_id, 7) ELSE '' END AS key) cursor
 WHERE d.tenant_id = $1 AND d.step = $2 AND NOT d.index_done`
	setPurgeTenant     = `SELECT set_config('marketplace.purge_tenant', $1, true)`
	advancePurgeCursor = `UPDATE marketplace_tenant_deletions
		SET step = $2,
			product_id = $3,
			index_done = $4,
			complete = $5
		WHERE tenant_id = $1`
	finishTenantPurge = `
		WITH participant AS (DELETE FROM participants WHERE tenant_id = $1)
		DELETE FROM tenants WHERE tenant_id = $1`
	deletePurgeCacheVersion = `DELETE FROM store_card_cache_versions WHERE tenant_id = $1`

	// Удалённый префикс служит курсором для таблиц без общего product_id.
	purgePageCTE = `%s AS (
		DELETE FROM %s WHERE ctid = ANY(ARRAY(%s)) RETURNING 1
	)`
	purgeTableKeys  = `SELECT ctid FROM %s WHERE tenant_id = $1 %s ORDER BY %s LIMIT $2 FOR UPDATE`
	purgeParentKeys = `SELECT ctid FROM (
		SELECT ctid, id FROM %s WHERE tenant_id = $1 ORDER BY id LIMIT $2 FOR UPDATE
	) parent WHERE true %s`
	purgeChildKeys = `
		SELECT items.ctid
		FROM (SELECT id FROM %s WHERE tenant_id = $1 ORDER BY id LIMIT $2) parent
		CROSS JOIN LATERAL (SELECT ctid FROM %s WHERE %s = parent.id LIMIT $2 FOR UPDATE) items LIMIT $2`
	purgeCaseTail  = `AND NOT EXISTS (SELECT 1 FROM moderation_decisions WHERE case_id = parent.id)`
	purgeReadyTask = `AND marketplace_store_task(kind)
		AND (attempts = 0 OR failed_at IS NOT NULL OR next_at <= statement_timestamp())`
	purgeResume = ` SELECT CASE WHEN (%s) > 0 THEN statement_timestamp() ELSE (
		SELECT next_at FROM outbox WHERE tenant_id = $1 AND marketplace_store_task(kind)
		ORDER BY next_at, id LIMIT 1
	) END`
	purgeCampaignTail = `
		AND NOT EXISTS (SELECT 1 FROM promotion_views WHERE campaign_id = parent.id)
		AND NOT EXISTS (SELECT 1 FROM promotion_counters WHERE campaign_id = parent.id)
		AND NOT EXISTS (SELECT 1 FROM promotion_decisions WHERE campaign_id = parent.id)`
)

var purgeTenantRows = tenantDeleteQuery()

type tenantPurge struct {
	db *gorm.DB
}

type tenantPurgeTable struct {
	name  string
	order string
	where string
}

type tenantPurgeChild struct {
	name   string
	parent string
	key    string
}

func (r tenantPurge) Available(ctx context.Context, tenants []ids.TenantID) (map[ids.TenantID]bool, error) {
	rows, err := rawSQL(ctx, r.db, openTenants, tenants).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	available := make(map[ids.TenantID]bool, len(tenants))
	for rows.Next() {
		var tenant ids.TenantID
		if err := rows.Scan(&tenant); err != nil {
			return nil, err
		}
		available[tenant] = true
	}
	return available, rows.Err()
}

func (r tenantPurge) Begin(ctx context.Context, tenant ids.TenantID) (bool, error) {
	count, err := execSQL(ctx, r.db, beginTenantPurge, tenant)
	return count > 0, err
}

func (r tenantPurge) LockCursor(ctx context.Context, tenant ids.TenantID) (ports.TenantPurgeCursor, error) {
	var cursor ports.TenantPurgeCursor
	err := rawSQL(ctx, r.db, lockPurgeCursor, tenant).Row().Scan(
		&cursor.Step,
		&cursor.Product,
		&cursor.IndexDone,
		&cursor.Complete,
	)
	return cursor, err
}

func (r tenantPurge) IndexPage(
	ctx context.Context, tenant ids.TenantID, step int64, limit int,
) ([]ids.ProductID, bool, error) {
	var encoded []byte
	err := rawSQL(ctx, r.db, purgeIndexProducts, tenant, step, limit).Row().Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var products []ids.ProductID
	err = json.Unmarshal(encoded, &products)
	return products, err == nil, err
}

func (r tenantPurge) DeletePage(ctx context.Context, tenant ids.TenantID, limit int) (time.Time, error) {
	if _, err := execSQL(ctx, r.db, setPurgeTenant, tenant); err != nil {
		return time.Time{}, err
	}
	var resume sql.NullTime
	err := rawSQL(ctx, r.db, purgeTenantRows, tenant, limit).Row().Scan(&resume)
	return resume.Time, err
}

func (r tenantPurge) Finish(ctx context.Context, tenant ids.TenantID) error {
	if _, err := execSQL(ctx, r.db, finishTenantPurge, tenant); err != nil {
		return err
	}
	_, err := execSQL(ctx, r.db, deletePurgeCacheVersion, tenant)
	return err
}

func (r tenantPurge) Advance(ctx context.Context, tenant ids.TenantID, cursor ports.TenantPurgeCursor) error {
	_, err := execSQL(
		ctx,
		r.db,
		advancePurgeCursor,
		tenant,
		cursor.Step,
		cursor.Product,
		cursor.IndexDone,
		cursor.Complete,
	)
	return err
}

func tenantDeleteQuery() string {
	tables := []tenantPurgeTable{
		{name: "catalog_products", order: "product_id"},
		{name: "offers", order: "product_id"},
		{name: "store_summary_products", order: "product_id"},
		{name: "store_summary_counts", order: "category, kind"},
		{name: "store_summary_changes", order: "id"},
		{name: "store_summary_change_heads", order: "tenant_id"},
		{name: "store_summary_change_cursor", order: "tenant_id"},
		{name: "store_summary_ratings", order: "tenant_id"},
		{name: "store_card_categories", order: "tenant_id"},
		{name: "participant_decisions", order: "at DESC"},
		{name: "moderation_verdicts", order: "product_id"},
		{name: "moderation_idempotency", order: "key_hash"},
		{name: "moderation_verdict_log", order: "product_id, at DESC, id DESC"},

		{name: "taxonomy_revalidation_failures", order: "product_id"},
		{name: "offer_index_tenant", order: "tenant_id"},
		{name: "catalog_tile_departures", order: "tile_key"},
		{name: "outbox", order: "next_at, id", where: purgeReadyTask},
		{name: "buyer_shop_subscriptions", order: "tenant_id"},
	}
	children := []tenantPurgeChild{
		{name: "moderation_decisions", parent: "moderation_cases", key: "case_id"},
		{name: "promotion_views", parent: "promotion_campaigns", key: "campaign_id"},
		{name: "promotion_counters", parent: "promotion_campaigns", key: "campaign_id"},
		{name: "promotion_decisions", parent: "promotion_campaigns", key: "campaign_id"},
	}
	ctes := make([]string, 0, len(tables)+len(children)+2)
	counts := make([]string, 0, len(ctes))
	add := func(table, keys string) {
		name := "purged_" + table
		ctes = append(ctes, fmt.Sprintf(purgePageCTE, name, table, keys))
		counts = append(counts, "(SELECT count(*) FROM "+name+")")
	}
	for _, table := range tables {
		add(table.name, fmt.Sprintf(purgeTableKeys, table.name, table.where, table.order))
	}
	for _, child := range children {
		add(child.name, fmt.Sprintf(purgeChildKeys, child.parent, child.name, child.key))
	}
	add("moderation_cases", fmt.Sprintf(purgeParentKeys, "moderation_cases", purgeCaseTail))
	add("promotion_campaigns", fmt.Sprintf(purgeParentKeys, "promotion_campaigns", purgeCampaignTail))
	return "WITH " + strings.Join(ctes, ",\n") + fmt.Sprintf(purgeResume, strings.Join(counts, " + "))
}
