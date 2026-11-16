package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ValidationSources — номера текущих источников и прежний результат, без схем и стоп-фраз.
type ValidationSources struct {
	Verdict         *moderation.Verdict
	TaxonomyVersion int64
	RulesRevision   int64
	Participant     bool
	Probation       bool
}

// ProjectionReuse — прежняя автоматическая оценка и открытое дело, если оно есть.
type ProjectionReuse struct {
	Verdict *moderation.Verdict
	Case    *moderation.Case
}

// ProjectionValidation согласует номера источников с записью проекции до конца транзакции.
type ProjectionValidation interface {
	Current(context.Context, ids.TenantID, ids.ProductID) (ValidationSources, error)
}

// ProjectionModerator разделяет новую оценку и перенос прежней автоматической оценки.
// Задачи проекции ставит Projector; остальные сценарии используют ModerationEvaluator.
type ProjectionModerator interface {
	EvaluateProjection(context.Context, Tx, *catalog.Product, *catalog.ProductState, time.Time) error
	PrepareReuse(context.Context, Tx, catalog.ProductState) (*ProjectionReuse, error)
	ReuseProjection(context.Context, Tx, *catalog.Product, *ProjectionReuse, time.Time) error
}
