package postgres

import (
	"context"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	selectTag = `
		SELECT tenant_id, tag_id, revision, deleted, value, version, updated_at
		FROM catalog_tags
		WHERE tenant_id = $1 AND tag_id = $2`

	insertTag = `
		INSERT INTO catalog_tags (tenant_id, tag_id, revision, deleted, value, version, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	updateTag = `
		UPDATE catalog_tags
		SET revision = $3,
			deleted = $4,
			value = $5,
			version = $6,
			updated_at = $7
		WHERE tenant_id = $1 AND tag_id = $2 AND version = $8`

	selectTagLabels = `
		SELECT tag_id, value
		FROM catalog_tags
		WHERE tenant_id = $1 AND tag_id = ANY($2) AND NOT deleted`
)

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

	err := r.q.QueryRow(ctx, selectTag, string(tenantID), string(tagID)).
		Scan(&tenant, &tag, &state.Revision, &state.Deleted, &state.Value, &state.Version, &state.UpdatedAt)
	if notFound(err) {
		return nil, catalog.ErrTagNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("тег %s/%s: %w", tenantID, tagID, err)
	}

	state.TenantID = ids.TenantID(tenant)
	state.TagID = ids.TagID(tag)

	return catalog.RestoreTag(state), nil
}

// Save заводит тег или обновляет его условно по версии.
func (r *Tags) Save(ctx context.Context, tag *catalog.Tag) error {
	s := tag.State()
	args := []any{
		string(s.TenantID),
		string(s.TagID),
		s.Revision,
		s.Deleted,
		s.Value,
		s.Version,
		s.UpdatedAt,
	}

	if tag.OriginVersion() == 0 {
		_, err := r.q.Exec(ctx, insertTag, args...)
		conflict := isUnique(err, "catalog_tags_pkey")

		return saved(err, conflict, tag.MarkSaved, "тег", s.TenantID, s.TagID)
	}

	result, err := r.q.Exec(ctx, updateTag, append(args, tag.OriginVersion())...)
	conflict := err == nil && result.RowsAffected() == 0

	return saved(err, conflict, tag.MarkSaved, "тег", s.TenantID, s.TagID)
}

// Labels — значения неудалённых тегов магазина.
func (r *Tags) Labels(ctx context.Context, tenantID ids.TenantID, tagIDs []string) (map[string]string, error) {
	rows, err := r.q.Query(ctx, selectTagLabels, string(tenantID), tagIDs)
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
