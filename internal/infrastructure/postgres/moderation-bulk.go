package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	bulkCached         = `SELECT response FROM moderation_bulk_idempotency WHERE key_hash = $1`
	bulkCache          = `INSERT INTO moderation_bulk_idempotency (key_hash, response) VALUES ($1, $2)`
	bulkProbationWhere = ` FROM moderation_cases WHERE tenant_id = $1 AND status = 'open'
        AND moderation_reason_keys(reasons) = ARRAY['moderation.probation']::text[]`
	bulkReasonsWhere = ` FROM moderation_cases WHERE tenant_id = $1 AND status = 'open'
        AND cardinality(moderation_reason_keys(reasons)) > 0 AND moderation_reason_keys(reasons) <@ $2::text[]`
	bulkPage             = `SELECT id, version`
	bulkPageAfter        = ` AND id > $2::uuid ORDER BY id LIMIT $3`
	bulkReasonsPageAfter = ` AND id > $3::uuid ORDER BY id LIMIT $4`
	// Товар → вердикт → дело: тот же порядок записи, что у проектора и одиночного решения.
	bulkLockProducts = `SELECT p.tenant_id FROM catalog_products p JOIN moderation_cases c
        USING (tenant_id, product_id) WHERE c.id = ANY($1::uuid[])
        ORDER BY p.tenant_id, p.product_id FOR SHARE OF p`
	bulkLockVerdicts = `SELECT v.tenant_id FROM moderation_verdicts v JOIN moderation_cases c
        USING (tenant_id, product_id) WHERE c.id = ANY($1::uuid[])
        ORDER BY v.tenant_id, v.product_id FOR UPDATE OF v`
	bulkRead = `SELECT to_jsonb(c) || jsonb_build_object('tenantId', c.tenant_id,
        'productId', c.product_id, 'openedAt', c.opened_at, 'decidedAt', c.decided_at),
        to_jsonb(v) || jsonb_build_object('operator', jsonb_build_object('id', v.operator_id, 'name', v.operator_name)),
        COALESCE(p.revision, 0), COALESCE(NOT p.deleted
            AND p.card->'marketplace' IS NOT NULL AND p.card->'marketplace' <> 'null'::jsonb
            AND (p.card->'publication'->>'published')::boolean, false)
        FROM moderation_cases c
        LEFT JOIN catalog_products p USING (tenant_id, product_id)
        LEFT JOIN moderation_verdicts v USING (tenant_id, product_id)
        WHERE c.id = ANY($1::uuid[]) ORDER BY c.tenant_id, c.product_id FOR UPDATE OF c`
	bulkMembers   = participantBatch + ` ORDER BY tenant_id FOR UPDATE`
	bulkDecisions = `INSERT INTO moderation_decisions
        (id, case_id, operator_id, operator_name, action, reason, reason_message, note, revision, at)
        SELECT id, "caseId", operator->>'id', operator->>'name', action, reason, "reasonMessage", note, revision, at
        FROM jsonb_to_recordset($1::jsonb) AS x(id uuid, "caseId" uuid, operator jsonb, action text,
            reason text, "reasonMessage" text, note text, revision bigint, at timestamptz)`
)

// BulkModeration хранит только ограниченную страницу решения оператора.
type BulkModeration struct {
	db *gorm.DB
}

func (s *scope) BulkModeration() ports.BulkModeration { return &BulkModeration{db: s.db} }

func bulkSelection(selector moderation.BulkSelector) (string, []any) {
	if slices.Equal(selector.Reasons, []string{"moderation.probation"}) {
		return bulkProbationWhere, []any{selector.TenantID}
	}
	return bulkReasonsWhere, []any{selector.TenantID, selector.Reasons}
}

// Page использует keyset; закрытие предыдущей страницы не сдвигает следующую.
func (r *BulkModeration) Page(
	ctx context.Context,
	selector moderation.BulkSelector,
	limit int,
) ([]moderation.BulkCase, error) {
	where, args := bulkSelection(selector)
	after := selector.After
	if after == "" {
		after = "00000000-0000-0000-0000-000000000000"
	}
	suffix := bulkPageAfter
	if len(args) == 2 {
		suffix = bulkReasonsPageAfter
	}
	args = append(args, after, limit)
	rows, err := rawSQL(ctx, r.db, bulkPage+where+suffix, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []moderation.BulkCase{}
	for rows.Next() {
		var value moderation.BulkCase
		if err := rows.Scan(&value.ID, &value.ExpectedVersion); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

// Count нужен подтверждению; сама обработка не считает очередь на каждой странице.
func (r *BulkModeration) Count(ctx context.Context, selector moderation.BulkSelector) (int, error) {
	where, args := bulkSelection(selector)
	var count int
	err := rawSQL(ctx, r.db, `SELECT count(*)`+where, args...).Row().Scan(&count)
	return count, err
}

// Read читает все дела одним запросом после блокировок связанных товаров и вердиктов.
func (r *BulkModeration) Read(
	ctx context.Context,
	cases []ids.UUID,
) (map[ids.UUID]ports.BulkModerationInput, error) {
	keys := uuidStrings(cases)
	for _, query := range []string{bulkLockProducts, bulkLockVerdicts} {
		if _, err := execSQL(ctx, r.db, query, keys); err != nil {
			return nil, err
		}
	}
	rows, err := rawSQL(ctx, r.db, bulkRead, keys).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := map[ids.UUID]ports.BulkModerationInput{}
	for rows.Next() {
		var rawCase, rawVerdict []byte
		var value ports.BulkModerationInput
		if err := rows.Scan(&rawCase, &rawVerdict, &value.Revision, &value.Published); err != nil {
			return nil, err
		}
		var c moderation.CaseState
		if err := json.Unmarshal(rawCase, &c); err != nil {
			return nil, err
		}
		value.Case = moderation.RestoreCase(c)
		if len(rawVerdict) > 0 {
			var v moderation.VerdictState
			if err := json.Unmarshal(rawVerdict, &v); err != nil {
				return nil, err
			}
			value.Verdict = moderation.RestoreVerdict(v)
		}
		result[c.ID] = value
	}
	return result, rows.Err()
}

// Members блокирует только участников страницы, не теряя параллельные одобрения.
func (r *BulkModeration) Members(
	ctx context.Context,
	tenants []ids.TenantID,
) (map[ids.TenantID]*participant.Participant, error) {
	keys := make([]string, 0, len(tenants))
	for _, tenant := range tenants {
		keys = append(keys, string(tenant))
	}
	rows, err := rawSQL(ctx, r.db, bulkMembers, keys).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := map[ids.TenantID]*participant.Participant{}
	for rows.Next() {
		value, err := scanParticipant(rows)
		if err != nil {
			return nil, err
		}
		result[value.TenantID()] = value
	}
	return result, rows.Err()
}

// Save пишет переходы, аудит и outbox наборами; исходные версии проверяются и под блокировкой.
func (r *BulkModeration) Save(
	ctx context.Context,
	changes []ports.BulkModerationChange,
	tasks []ports.NewTask,
) error {
	verdicts, cases, decisions := []revalidationVerdictWrite{}, []revalidationCaseWrite{}, []moderation.Decision{}
	for _, change := range changes {
		v, c := change.Verdict.Origin(), change.Case.Origin()
		verdicts = append(verdicts, revalidationVerdictWrite{
			VerdictState: change.Verdict.State(),
			Origin:       v.Version,
			OriginStatus: v.Status,
		})
		cases = append(cases, revalidationCaseWrite{
			CaseState:    change.Case.State(),
			Origin:       c.Version,
			OriginStatus: c.Status,
		})
		decisions = append(decisions, change.Decision)
	}
	for _, batch := range []struct {
		query string
		value any
	}{
		{query: saveRevalidationVerdicts, value: verdicts},
		{query: saveRevalidationCases, value: cases},
		{query: bulkDecisions, value: decisions},
	} {
		raw, err := json.Marshal(batch.value)
		if err != nil {
			return err
		}
		count, err := execSQL(ctx, r.db, batch.query, string(raw))
		if err != nil {
			return err
		}
		if count != int64(len(changes)) {
			return moderation.ErrConflict
		}
	}
	writes := []revalidationTaskWrite{}
	for _, task := range tasks {
		next := task.AvailableAt
		if next.IsZero() {
			next = task.At
		}
		writes = append(writes, revalidationTaskWrite{
			NextAt:  next,
			Kind:    task.Kind,
			Tenant:  string(task.TenantID),
			Dedup:   task.DedupKey,
			Payload: task.Payload,
			At:      task.At,
		})
	}
	raw, err := json.Marshal(writes)
	if err != nil {
		return err
	}
	_, err = execSQL(ctx, r.db, insertRevalidationTasks, string(raw))
	return err
}

// Cached удерживает блокировку ключа до атомарного сохранения всей страницы.
func (r *BulkModeration) Cached(ctx context.Context, key string) (ports.CachedBulkApproval, bool, error) {
	var value ports.CachedBulkApproval
	if _, err := execSQL(ctx, r.db, lockModerationDecision, key); err != nil {
		return value, false, err
	}
	var raw []byte
	err := rawSQL(ctx, r.db, bulkCached, key).Row().Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	if err != nil {
		return value, false, err
	}
	err = json.Unmarshal(raw, &value)
	return value, err == nil, err
}

// Cache хранит исходный результат без TTL: повтор отбора никогда не выбирает новые дела.
func (r *BulkModeration) Cache(ctx context.Context, key string, value ports.CachedBulkApproval) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = execSQL(ctx, r.db, bulkCache, key, raw)
	return err
}
