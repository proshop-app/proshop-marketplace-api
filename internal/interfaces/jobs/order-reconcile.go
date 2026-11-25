package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	// OrderReconcileInterval — общий суточный интервал сверки для всех реплик.
	OrderReconcileInterval        = 24 * time.Hour
	orderReconcileAttemptInterval = time.Hour
)

// OrderReconciler — суточная сверка статусов заказов пачкой.
type OrderReconciler interface {
	ReconcileOrders(ctx context.Context) (appcheckout.ReconcileResult, error)
}

// OrderReconcileRunner проверяет готовность суточной сверки раз в час.
type OrderReconcileRunner struct {
	reconciler OrderReconciler
	schedule   ports.IndexRebuildLock
	logger     *slog.Logger
	orders     *prometheus.CounterVec
	failures   *prometheus.CounterVec
}

// NewOrderReconcileRunner — сверка со счётчиком расхождений: ненулевой рост означает пропущенные события.
func NewOrderReconcileRunner(
	reconciler OrderReconciler,
	schedule ports.IndexRebuildLock,
	logger *slog.Logger,
	registerer prometheus.Registerer,
) *OrderReconcileRunner {
	r := &OrderReconcileRunner{
		reconciler: reconciler,
		schedule:   schedule,
		logger:     logger,
		orders: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_order_reconcile_orders_total",
				Help: "Заказы суточной сверки по исходу: совпал, разошёлся, неизвестен магазину, не исправлен.",
			},
			[]string{"outcome"},
		),
		failures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_order_reconcile_failures_total",
				Help: "Магазины, не ответившие на суточную сверку статусов заказов.",
			},
			[]string{"tenant"},
		),
	}
	registerer.MustRegister(r.orders, r.failures)

	return r
}

// Run пробует запустить сверку сразу и затем каждый час до отмены контекста.
func (r *OrderReconcileRunner) Run(ctx context.Context) error {
	return r.run(ctx, wait)
}

func (r *OrderReconcileRunner) run(
	ctx context.Context,
	waitNextAttempt func(context.Context, time.Duration) bool,
) error {
	for {
		var result appcheckout.ReconcileResult
		err := r.schedule.WithLock(ctx, func(ctx context.Context) error {
			var err error
			result, err = r.reconciler.ReconcileOrders(ctx)
			return err
		})

		switch {
		case errors.Is(err, ports.ErrRebuildBusy):
		case err != nil && ctx.Err() == nil:
			r.logger.Error("сверка заказов: не удалась", slog.Any("error", err))
		case err == nil:
			r.record(result)
		}

		if !waitNextAttempt(ctx, orderReconcileAttemptInterval) {
			return nil
		}
	}
}

func (r *OrderReconcileRunner) record(result appcheckout.ReconcileResult) {
	matched := result.Checked - result.Mismatched - result.Missing - result.Unresolved
	r.orders.WithLabelValues("matched").Add(float64(matched))
	r.orders.WithLabelValues("mismatched").Add(float64(result.Mismatched))
	r.orders.WithLabelValues("missing").Add(float64(result.Missing))
	r.orders.WithLabelValues("unresolved").Add(float64(result.Unresolved))
	for tenant, cause := range result.Failed {
		r.failures.WithLabelValues(string(tenant)).Inc()
		r.logger.Warn(
			"сверка заказов: магазин не ответил",
			slog.String("tenantId", string(tenant)),
			slog.Any("error", cause),
		)
	}
	r.logger.Info(
		"сверка заказов: завершена",
		slog.Int("checked", result.Checked),
		slog.Int("mismatched", result.Mismatched),
		slog.Int("missing", result.Missing),
		slog.Int("unresolved", result.Unresolved),
		slog.Int("failed", len(result.Failed)),
	)
}
