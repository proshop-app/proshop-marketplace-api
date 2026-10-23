package jobs

import (
	"context"
	"log/slog"
	"time"
)

// OrderResumer — возобновление саги и сверка статусов магазинов.
type OrderResumer interface{ Resume(context.Context) error }

// OrdersRunner запускает сверку сразу и по расписанию.
type OrdersRunner struct {
	service  OrderResumer
	interval time.Duration
	logger   *slog.Logger
}

// NewOrdersRunner собирает цикл восстановления покупок.
func NewOrdersRunner(service OrderResumer, interval time.Duration, logger *slog.Logger) *OrdersRunner {
	return &OrdersRunner{service: service, interval: interval, logger: logger}
}

// Run восстанавливает незаконченные создания на старте, затем сверяет статусы по расписанию.
func (r *OrdersRunner) Run(ctx context.Context) error {
	for {
		if err := r.service.Resume(ctx); err != nil && ctx.Err() == nil {
			r.logger.Error("сверка заказов магазинов", slog.Any("error", err))
		}
		if !wait(ctx, r.interval) {
			return nil
		}
	}
}
