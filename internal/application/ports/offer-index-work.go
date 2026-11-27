package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ErrOfferIndexProgressConflict означает, что другой обработчик уже продвинул курсор.
var ErrOfferIndexProgressConflict = errs.New(
	errs.Conflict,
	"catalog.index_progress_conflict",
	"позиция индексации уже изменилась",
)

// IndexRebuildProgress — подтверждённый префикс основного обхода поколения.
type IndexRebuildProgress struct {
	Generation string
	Started    time.Time
	After      OfferKey
	Indexed    int
	Skipped    int
	Complete   bool
}

// TenantIndexProgress — подтверждённая позиция обхода одной версии магазина.
type TenantIndexProgress struct {
	Revision int64
	Target   int64
	After    ids.ProductID
	Complete bool
}

// OfferIndexWork хранит позиции фоновых обходов.
type OfferIndexWork interface {
	BeginRebuild(context.Context, string, time.Time) (IndexRebuildProgress, error)
	SaveRebuild(context.Context, IndexRebuildProgress, IndexRebuildProgress) error
	TenantProgress(context.Context, ids.TenantID) (TenantIndexProgress, int64, error)
	SaveTenant(context.Context, ids.TenantID, TenantIndexProgress) error
}

// OfferIndexSkipObserver учитывает пропуски подтверждённых страниц фоновых обходов.
type OfferIndexSkipObserver interface {
	ObserveIndexSkips(phase string, count int)
}
