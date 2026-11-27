package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

const (
	revalidationSavepoint      = `SAVEPOINT revalidation_write`
	revalidationRollback       = `ROLLBACK TO SAVEPOINT revalidation_write`
	revalidationRelease        = `RELEASE SAVEPOINT revalidation_write`
	updateRevalidationProducts = `UPDATE catalog_products p
        SET card = x.card,
            version = x.version,
            updated_at = x.updated_at
        FROM jsonb_to_recordset($1::jsonb) AS x(tenant_id text, product_id text, card jsonb,
            version bigint, updated_at timestamptz, origin bigint)
        WHERE p.tenant_id = x.tenant_id AND p.product_id = x.product_id AND p.version = x.origin`
	saveRevalidationVerdicts = `INSERT INTO moderation_verdicts AS v
        (tenant_id, product_id, revision, rules_revision, taxonomy_version, status, reasons,
            decided_by, manual_reject, version, updated_at, operator_id, operator_name, decision)
        SELECT tenant_id, product_id, revision, rules_revision, taxonomy_version, status, reasons,
            decided_by, manual_reject, version, updated_at, operator->>'id', operator->>'name', decision
        FROM jsonb_to_recordset($1::jsonb) AS x(tenant_id text, product_id text, revision bigint,
            rules_revision bigint, taxonomy_version bigint, status text, reasons jsonb, decided_by text,
            manual_reject boolean, version bigint, updated_at timestamptz, operator jsonb, decision jsonb,
            origin bigint, origin_status text)
        ON CONFLICT (tenant_id, product_id) DO UPDATE
        SET revision = EXCLUDED.revision,
            rules_revision = EXCLUDED.rules_revision,
            taxonomy_version = EXCLUDED.taxonomy_version,
            status = EXCLUDED.status,
            reasons = EXCLUDED.reasons,
            decided_by = EXCLUDED.decided_by,
            manual_reject = EXCLUDED.manual_reject,
            version = EXCLUDED.version,
            updated_at = EXCLUDED.updated_at,
            operator_id = EXCLUDED.operator_id,
            operator_name = EXCLUDED.operator_name,
            decision = EXCLUDED.decision
        WHERE EXISTS (SELECT 1 FROM jsonb_to_recordset($1::jsonb) AS e(tenant_id text, product_id text,
            origin bigint, origin_status text) WHERE e.tenant_id = v.tenant_id AND e.product_id = v.product_id
            AND e.origin > 0 AND v.version = e.origin AND v.status = e.origin_status)`
	saveRevalidationCases = `INSERT INTO moderation_cases AS c
        (id, tenant_id, product_id, revision, source, status, reasons, note, opened_at, decided_at, version, card)
        SELECT id, "tenantId", "productId", revision, source, status, reasons, note,
            "openedAt", "decidedAt", version, card
        FROM jsonb_to_recordset($1::jsonb) AS x(id uuid, "tenantId" text, "productId" text, revision bigint,
            source text, status text, reasons jsonb, note text, "openedAt" timestamptz, "decidedAt" timestamptz,
            version bigint, card jsonb, origin bigint, origin_status text)
        ON CONFLICT (id) DO UPDATE
        SET revision = EXCLUDED.revision,
            source = EXCLUDED.source,
            status = EXCLUDED.status,
            reasons = EXCLUDED.reasons,
            note = EXCLUDED.note,
            opened_at = EXCLUDED.opened_at,
            decided_at = EXCLUDED.decided_at,
            version = EXCLUDED.version,
            card = EXCLUDED.card
        WHERE EXISTS (SELECT 1 FROM jsonb_to_recordset($1::jsonb) AS e(id uuid, origin bigint, origin_status text)
            WHERE e.id = c.id AND e.origin > 0 AND c.version = e.origin AND c.status = e.origin_status)`
	insertRevalidationTasks = `INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
        SELECT kind, tenant, NULLIF(dedup, ''), payload, next_at, at
        FROM jsonb_to_recordset($1::jsonb) AS x(kind text, tenant text, dedup text,
            payload jsonb, next_at timestamptz, at timestamptz)
        ` + mergePendingTasks
)

type revalidationProductWrite struct {
	Tenant    string          `json:"tenant_id"`
	Product   string          `json:"product_id"`
	Card      json.RawMessage `json:"card"`
	Version   int64           `json:"version"`
	UpdatedAt time.Time       `json:"updated_at"`
	Origin    int64           `json:"origin"`
}

type revalidationVerdictWrite struct {
	moderation.VerdictState
	Origin       int64             `json:"origin"`
	OriginStatus moderation.Status `json:"origin_status"`
}

type revalidationCaseWrite struct {
	moderation.CaseState
	Origin       int64             `json:"origin"`
	OriginStatus moderation.Status `json:"origin_status"`
}

type revalidationTaskWrite struct {
	NextAt  time.Time       `json:"next_at"`
	Kind    string          `json:"kind"`
	Tenant  string          `json:"tenant"`
	Dedup   string          `json:"dedup"`
	Payload json.RawMessage `json:"payload"`
	At      time.Time       `json:"at"`
}

type preparedRevalidation struct {
	change  ports.RevalidationChange
	product json.RawMessage
	verdict json.RawMessage
	review  json.RawMessage
}

// Save пишет по одному UPDATE/INSERT на каждый набор страницы. SAVEPOINT позволяет
// изолировать конкретную постоянную ошибку данных, не проглатывая ошибку прерванной транзакции.
func (r *Revalidations) Save(
	ctx context.Context,
	changes []ports.RevalidationChange,
) ([]ports.RevalidationFailure, error) {
	prepared := []preparedRevalidation{}
	failures := []ports.RevalidationFailure{}
	for _, change := range changes {
		value, err := prepareRevalidation(change)
		if err != nil {
			failures = append(failures, revalidationWriteFailure(change, "encode: "+err.Error()))
			continue
		}
		prepared = append(prepared, value)
	}
	failed, err := r.saveRevalidation(ctx, prepared)
	return append(failures, failed...), err
}

func prepareRevalidation(change ports.RevalidationChange) (preparedRevalidation, error) {
	result := preparedRevalidation{change: change}
	state := change.Product.State()
	card, err := encodeCard(state.Card)
	if err != nil {
		return result, err
	}
	result.product, err = json.Marshal(revalidationProductWrite{
		Tenant:    string(state.TenantID),
		Product:   string(state.ProductID),
		Card:      card,
		Version:   state.Version,
		UpdatedAt: state.UpdatedAt,
		Origin:    change.Product.OriginVersion(),
	})
	if err != nil {
		return result, err
	}
	if change.Verdict != nil {
		origin := change.Verdict.Origin()
		result.verdict, err = json.Marshal(revalidationVerdictWrite{
			VerdictState: change.Verdict.State(),
			Origin:       origin.Version,
			OriginStatus: origin.Status,
		})
		if err != nil {
			return result, err
		}
	}
	if change.Case != nil {
		origin := change.Case.Origin()
		result.review, err = json.Marshal(revalidationCaseWrite{
			CaseState:    change.Case.State(),
			Origin:       origin.Version,
			OriginStatus: origin.Status,
		})
	}
	return result, err
}

func (r *Revalidations) saveRevalidation(
	ctx context.Context,
	items []preparedRevalidation,
) ([]ports.RevalidationFailure, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if _, err := execSQL(ctx, r.db, revalidationSavepoint); err != nil {
		return nil, err
	}
	err := r.writeRevalidation(ctx, items)
	if err == nil {
		return nil, r.releaseRevalidation(ctx, items)
	}
	if !permanentRevalidationSQL(err) {
		return nil, err
	}
	if _, rollbackErr := execSQL(ctx, r.db, revalidationRollback); rollbackErr != nil {
		return nil, rollbackErr
	}
	if _, releaseErr := execSQL(ctx, r.db, revalidationRelease); releaseErr != nil {
		return nil, releaseErr
	}
	if len(items) == 1 {
		return []ports.RevalidationFailure{revalidationWriteFailure(items[0].change, "save: "+err.Error())}, nil
	}
	left, err := r.saveRevalidation(ctx, items[:len(items)/2])
	if err != nil {
		return nil, err
	}
	right, err := r.saveRevalidation(ctx, items[len(items)/2:])
	return append(left, right...), err
}

func (r *Revalidations) releaseRevalidation(ctx context.Context, items []preparedRevalidation) error {
	if _, err := execSQL(ctx, r.db, revalidationRelease); err != nil {
		return err
	}
	for _, item := range items {
		item.change.Product.MarkSaved()
		if item.change.Verdict != nil {
			item.change.Verdict.MarkSaved()
		}
		if item.change.Case != nil {
			item.change.Case.MarkSaved()
		}
	}
	return nil
}

func (r *Revalidations) writeRevalidation(ctx context.Context, items []preparedRevalidation) error {
	products, verdicts, cases, tasks := revalidationWrites(items)
	for _, batch := range []struct {
		query string
		rows  []json.RawMessage
	}{
		{query: updateRevalidationProducts, rows: products},
		{query: saveRevalidationVerdicts, rows: verdicts},
		{query: saveRevalidationCases, rows: cases},
	} {
		if err := r.writeRevalidationRows(ctx, batch.query, batch.rows); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(tasks)
	if err != nil {
		return err
	}
	_, err = execSQL(ctx, r.db, insertRevalidationTasks, string(raw))
	return err
}

func (r *Revalidations) writeRevalidationRows(ctx context.Context, query string, rows []json.RawMessage) error {
	if len(rows) == 0 {
		return nil
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	count, err := execSQL(ctx, r.db, query, string(raw))
	if err != nil {
		return err
	}
	if count != int64(len(rows)) {
		return catalog.ErrConflict
	}
	return nil
}

func revalidationWrites(items []preparedRevalidation) (
	[]json.RawMessage,
	[]json.RawMessage,
	[]json.RawMessage,
	[]revalidationTaskWrite,
) {
	products, verdicts, cases := []json.RawMessage{}, []json.RawMessage{}, []json.RawMessage{}
	tasks := []revalidationTaskWrite{}
	seen := map[string]bool{}
	for _, item := range items {
		products = append(products, item.product)
		if item.verdict != nil {
			verdicts = append(verdicts, item.verdict)
		}
		if item.review != nil {
			cases = append(cases, item.review)
		}
		for _, task := range item.change.Tasks {
			if task.DedupKey != "" && seen[task.DedupKey] {
				continue
			}
			seen[task.DedupKey] = true
			next := task.AvailableAt
			if next.IsZero() {
				next = task.At
			}
			tasks = append(tasks, revalidationTaskWrite{
				NextAt:  next,
				Kind:    task.Kind,
				Tenant:  string(task.TenantID),
				Dedup:   task.DedupKey,
				Payload: task.Payload,
				At:      task.At,
			})
		}
	}
	return products, verdicts, cases, tasks
}

func permanentRevalidationSQL(err error) bool {
	// Класс 22 — ошибка значения/кодирования строки; 23502/23503/23514 — нарушение
	// NOT NULL, внешнего ключа или CHECK. Повтор той же карточки их не исправит.
	// 23505 может быть гонкой вставки; 40001 (сериализация) и 40P01 (deadlock)
	// требуют повтора всей транзакции, сохраняя прежний курсор и атомарность страницы.
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) {
		return false
	}
	return len(pgError.Code) == 5 && (pgError.Code[:2] == "22" ||
		pgError.Code == "23514" || pgError.Code == "23502" || pgError.Code == "23503")
}

func revalidationWriteFailure(change ports.RevalidationChange, diagnostic string) ports.RevalidationFailure {
	state := change.Product.State()
	return ports.RevalidationFailure{
		TenantID:        state.TenantID,
		ProductID:       state.ProductID,
		ProductVersion:  change.Product.OriginVersion(),
		SourceRevision:  state.Revision,
		TaxonomyVersion: state.Card.Publication.Version,
		Diagnostic:      diagnostic,
		At:              state.UpdatedAt,
	}
}
