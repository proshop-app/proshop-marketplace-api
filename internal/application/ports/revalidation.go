package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// RevalidationItem сохраняет ключ даже при повреждённом JSON проекции.
type RevalidationItem struct {
	Revision int64
	Key      ids.OfferID
	Version  int64
	Product  *catalog.Product
	Failure  string
}

// RevalidationModeration — данные только выбранной страницы, без повторного чтения товаров.
type RevalidationModeration struct {
	Verdict *moderation.Verdict
	Case    *moderation.Case
	Signals *catalog.Signals
	Failure string
}

// RevalidationChange — план одной карточки. Адаптер атомарно сохраняет все его части.
type RevalidationChange struct {
	Observations []func()
	Product      *catalog.Product
	Verdict      *moderation.Verdict
	Case         *moderation.Case
	Tasks        []NewTask
	Failure      string
}

// RevalidationFailure виден оператору, пока исходная версия товара не исправлена.
type RevalidationFailure struct {
	SourceRevision  int64         `json:"sourceRevision"`
	TenantID        ids.TenantID  `json:"tenantId"`
	ProductID       ids.ProductID `json:"productId"`
	ProductVersion  int64         `json:"productVersion"`
	TaxonomyVersion int64         `json:"taxonomyVersion"`
	Diagnostic      string        `json:"diagnostic"`
	At              time.Time     `json:"at"`
}

// RevalidationStore ограничивает пакетную запись и изоляцию постоянной ошибки одной карточки.
type RevalidationStore interface {
	Page(context.Context, taxonomy.Revalidation, int) ([]RevalidationItem, error)
	Moderation(context.Context, []ids.OfferID) (map[ids.OfferID]RevalidationModeration, error)
	Save(context.Context, []RevalidationChange) ([]RevalidationFailure, error)
	RecordFailures(context.Context, []RevalidationFailure) error
	Failures(context.Context, OfferKey, int) ([]RevalidationFailure, error)
}

// RevalidationEvaluator готовит оценку по уже прочитанному определению и общим данным страницы.
type RevalidationEvaluator interface {
	PlanRevalidation(
		context.Context,
		Tx,
		[]*catalog.Product,
		taxonomy.Definition,
		taxonomy.Checker,
		time.Time,
	) ([]RevalidationChange, error)
}
