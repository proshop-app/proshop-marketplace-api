package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

const taxonomyProgressPages = 100

// TaxonomyRevalidator обрабатывает одну устойчивую страницу за вызов.
type TaxonomyRevalidator interface {
	RevalidateNext(context.Context) (*taxonomy.Revalidation, error)
}

// RunTaxonomyRevalidation не использует очередь событий: одна страница и пауза уступают базу приёму.
func RunTaxonomyRevalidation(ctx context.Context, service TaxonomyRevalidator, logger *slog.Logger) error {
	var pages int64
	for {
		job, err := service.RevalidateNext(ctx)
		delay := time.Second
		if err != nil && ctx.Err() == nil {
			logger.Error("перепроверка таксономии отложена", slog.Any("error", err))
		}
		if err == nil && job != nil {
			delay = 100 * time.Millisecond
			pages++
			logTaxonomyProgress(ctx, logger, *job, pages)
		}
		if !wait(ctx, delay) {
			return nil
		}
	}
}

func logTaxonomyProgress(ctx context.Context, logger *slog.Logger, job taxonomy.Revalidation, pages int64) {
	level := slog.LevelDebug
	message := "перепроверка таксономии: страница сохранена"
	if job.Done || pages%taxonomyProgressPages == 0 {
		level = slog.LevelInfo
		message = "перепроверка таксономии: прогресс"
	}
	if job.Done {
		message = "перепроверка таксономии: категория завершена"
	}
	logger.LogAttrs(
		ctx,
		level,
		message,
		slog.Int64("pages", pages),
		slog.Int64("version", job.Version),
		slog.String("category", job.Category),
		slog.String("tenantId", job.TenantCursor),
		slog.String("productId", job.ProductCursor),
		slog.Int64("processed", job.Processed),
		slog.Bool("done", job.Done),
	)
}
