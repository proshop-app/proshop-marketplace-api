package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

const taxonomyProgressPages = 100

// TaxonomyRevalidator обрабатывает одну устойчивую страницу за вызов.
type TaxonomyRevalidator interface {
	RevalidateNext(context.Context) (*taxonomy.Revalidation, error)
}

// RunTaxonomyWork ждёт ближайшую страницу или подсказку о публикации.
func RunTaxonomyWork(
	ctx context.Context,
	service TaxonomyRevalidator,
	deadline workDeadline,
	watch ports.WorkWatch,
	logger *slog.Logger,
	clock ports.Clock,
) error {
	recovery := time.NewTicker(WorkRecovery)
	defer recovery.Stop()
	var pages int64
	var retry workRetry
	for ctx.Err() == nil {
		next, err := deadline(ctx)
		now := clock.Now()
		if err == nil && !next.IsZero() {
			next = laterTime(next, retry.until)
			if isDue(next, now) {
				job, pageErr := service.RevalidateNext(ctx)
				err = pageErr
				retry.afterAttempt(clock.Now(), job != nil, err)
				next = retry.until
				if job != nil {
					pages++
					logTaxonomyProgress(ctx, logger, *job, pages)
					next = clock.Now().Add(100 * time.Millisecond)
					retry.until = next
				}
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			logger.Error("перепроверка таксономии отложена", slog.Any("error", err))
			next = clock.Now().Add(time.Second)
		}
		if !waitWork(ctx, clock, next, watch, recovery.C) {
			break
		}
	}
	return nil
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
