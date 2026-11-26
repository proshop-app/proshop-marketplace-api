package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// OrderResumer — сверка статусов магазинов и повтор возврата товаров.
type OrderResumer interface{ Resume(context.Context) error }

// OrdersRunner запускает восстановление покупок сразу и по расписанию.
type OrdersRunner struct {
	schedule ports.IntervalWork
	service  OrderResumer
	interval time.Duration
	logger   *slog.Logger
}

// NewOrdersRunner собирает цикл восстановления покупок.
func NewOrdersRunner(service OrderResumer, interval time.Duration, logger *slog.Logger) *OrdersRunner {
	return &OrdersRunner{service: service, interval: interval, logger: logger}
}

// WithSchedule назначает общий интервал для всех реплик: проход за интервал делает одна.
func (r *OrdersRunner) WithSchedule(schedule ports.IntervalWork) *OrdersRunner {
	r.schedule = schedule
	return r
}

// Run восстанавливает покупки сразу при старте и затем по расписанию.
func (r *OrdersRunner) Run(ctx context.Context) error {
	for {
		err := r.resume(ctx)
		if err != nil && !errors.Is(err, ports.ErrIntervalTaken) && ctx.Err() == nil {
			r.logger.Error("восстановление покупок", slog.Any("error", err))
		}
		if !wait(ctx, r.interval) {
			return nil
		}
	}
}

func (r *OrdersRunner) resume(ctx context.Context) error {
	if r.schedule == nil {
		return r.service.Resume(ctx)
	}
	return r.schedule.Run(ctx, r.service.Resume)
}
