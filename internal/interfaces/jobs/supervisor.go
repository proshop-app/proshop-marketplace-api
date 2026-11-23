package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// Loop — именованный фоновый цикл процесса; имя попадает в журнал и метрику паник.
type Loop struct {
	Name string
	Run  func(context.Context) error
}

// Supervisor — единственное место перехвата паники фоновых циклов: паника одного
// цикла не завершает процесс и не останавливает HTTP, приём и остальные циклы.
type Supervisor struct {
	logger *slog.Logger
	panics *prometheus.CounterVec
}

// NewSupervisor регистрирует метрику паник фоновых циклов.
func NewSupervisor(logger *slog.Logger, registerer prometheus.Registerer) *Supervisor {
	panics := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "marketplace_job_panics_total",
			Help: "Паники фоновых циклов; цикл продолжает работу после паузы.",
		},
		[]string{"job"},
	)
	registerer.MustRegister(panics)

	return &Supervisor{logger: logger, panics: panics}
}

// Run выполняет цикл до его возврата. После паники цикл запускается заново с паузой:
// повторяющаяся паника растит паузу до messaging.MaxBackoff, чтобы не крутить ту же
// работу и не засыпать журнал стеками; цикл, проработавший дольше паузы, начинает отсчёт заново.
func (s *Supervisor) Run(ctx context.Context, loop Loop) error {
	failures := 0
	for {
		started := time.Now()
		err := errs.Guard(func() error { return loop.Run(ctx) })
		stack, panicked := errs.PanicStack(err)
		if !panicked {
			return err
		}
		if time.Since(started) > messaging.Backoff(failures) {
			failures = 0
		}
		failures++
		pause := messaging.Backoff(failures)
		s.panics.WithLabelValues(loop.Name).Inc()
		s.logger.Error(
			"фоновый цикл: паника, продолжение после паузы",
			slog.String("job", loop.Name),
			slog.Duration("pause", pause),
			slog.Any("error", err),
			slog.String("stack", string(stack)),
		)
		if !wait(ctx, pause) {
			return nil
		}
	}
}
