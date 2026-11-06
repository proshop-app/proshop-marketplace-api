package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apppromotion "github.com/proshop-app/proshop-marketplace/internal/application/promotion"
)

// PromotionRunner проверяет сроки под общей блокировкой процессов.
type PromotionRunner struct {
	service  *apppromotion.Service
	lock     ports.IndexRebuildLock
	logger   *slog.Logger
	interval time.Duration
}

// NewPromotionRunner задаёт общий цикл активации и завершения.
func NewPromotionRunner(
	s *apppromotion.Service,
	lock ports.IndexRebuildLock,
	logger *slog.Logger,
	interval time.Duration,
) *PromotionRunner {
	return &PromotionRunner{service: s, lock: lock, logger: logger, interval: interval}
}

// Run повторяет переходы с заданным интервалом без остановки сервиса при временной ошибке.
func (r *PromotionRunner) Run(ctx context.Context) error {
	for {
		err := r.lock.WithLock(ctx, r.service.Tick)
		if err != nil && !errors.Is(err, ports.ErrRebuildBusy) && ctx.Err() == nil {
			r.logger.Error("переходы продвижения отложены", slog.Any("error", err))
		}
		if !wait(ctx, r.interval) {
			return nil
		}
	}
}
