package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

// ModerationEvaluator исключает зависимость каталога от реализации сценария модерации.
type ModerationEvaluator interface {
	Evaluate(context.Context, Tx, *catalog.Product, *participant.Participant, time.Time) error
}
