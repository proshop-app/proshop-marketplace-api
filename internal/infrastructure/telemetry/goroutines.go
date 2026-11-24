package telemetry

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/safego"
)

// NewGoroutinePanics — получатель паник защищённых горутин процесса: стек в журнал
// и счётчик по имени горутины. Имена заданы в коде, число меток ограничено.
func NewGoroutinePanics(registerer prometheus.Registerer, logger *slog.Logger) safego.Reporter {
	panics := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "marketplace_goroutine_panics_total",
			Help: "Паники защищённых горутин; ожидающий получил ошибку, процесс продолжает работу.",
		},
		[]string{"goroutine"},
	)
	registerer.MustRegister(panics)

	return func(name string, err error) {
		panics.WithLabelValues(name).Inc()
		stack, _ := errs.PanicStack(err)
		logger.Error(
			"горутина: паника",
			slog.String("goroutine", name),
			slog.Any("error", err),
			slog.String("stack", string(stack)),
		)
	}
}
