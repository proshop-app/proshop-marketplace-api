package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// TaxonomyRevalidator обрабатывает одну устойчивую страницу за вызов.
type TaxonomyRevalidator interface {
	RevalidateNext(context.Context) (*taxonomy.Revalidation, error)
}

// RunTaxonomyRevalidation не использует очередь событий: одна страница и пауза уступают базу приёму.
func RunTaxonomyRevalidation(ctx context.Context, service TaxonomyRevalidator, logger *slog.Logger) error {
	for {
		job, err := service.RevalidateNext(ctx)
		delay := time.Second
		if err != nil && ctx.Err() == nil {
			logger.Error("перепроверка таксономии отложена", slog.Any("error", err))
		}
		if err == nil && job != nil {
			delay = 100 * time.Millisecond
			logger.Info(
				"перепроверка таксономии: страница сохранена",
				slog.Int64("version", job.Version),
				slog.String("category", job.Category),
				slog.String("tenantId", job.TenantCursor),
				slog.String("productId", job.ProductCursor),
				slog.Int64("processed", job.Processed),
				slog.Bool("done", job.Done),
			)
		}
		if !wait(ctx, delay) {
			return nil
		}
	}
}
