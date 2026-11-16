package jobs

import (
	"context"
	"log/slog"
	"time"
)

// OrderResumer — сверка статусов магазинов и повтор возврата товаров.
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

// Run ставит сверку в outbox сразу при старте и затем по расписанию.
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
