package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

// ModerationEvaluator исключает зависимость каталога от реализации сценария модерации.
type ModerationEvaluator interface {
	Evaluate(context.Context, Tx, *catalog.Product, *participant.Participant, *catalog.ProductState, time.Time) error
}

// ModerationMetrics отделяет наблюдение сценариев от библиотеки Prometheus.
type ModerationMetrics interface {
	Verdict(moderation.VerdictState)
	Decision(moderation.Action)
}
