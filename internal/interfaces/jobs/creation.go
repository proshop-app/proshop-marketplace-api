package jobs

import (
	"context"
	"log/slog"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
)

// Малый интервал нужен только очереди; история покупок здесь не сканируется.
const creationPollInterval = 200 * time.Millisecond

// CreationRunner пополняет пул после каждого результата, независимо от других задач outbox.
type CreationRunner struct {
	executor *appcheckout.Executor
	logger   *slog.Logger
	record   func(messaging.Outcome)
}

// NewCreationRunner подключает пул к жизненному циклу процесса.
func NewCreationRunner(
	executor *appcheckout.Executor,
	logger *slog.Logger,
	record func(messaging.Outcome),
) *CreationRunner {
	return &CreationRunner{executor: executor, logger: logger, record: record}
}

// Run пополняет свободные места до остановки процесса.
func (r *CreationRunner) Run(ctx context.Context) error {
	ticker := time.NewTicker(creationPollInterval)
	defer ticker.Stop()
	return r.run(ctx, ticker.C)
}

func (r *CreationRunner) run(ctx context.Context, ticks <-chan time.Time) error {
	defer r.executor.Wait()
	failures := 0
	kind := ""
	for {
		var err error
		if kind == "" {
			err = r.executor.Dispatch(ctx)
		} else {
			err = r.executor.Refill(ctx, kind)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			r.logger.Error("создание: очередь недоступна", slog.Any("error", err))
			if kind == "" {
				failures++
				if !wait(ctx, messaging.Backoff(failures)) {
					return nil
				}
				continue
			}
		} else if kind == "" {
			failures = 0
		}
		select {
		case <-ctx.Done():
			return nil
		case result := <-r.executor.Results():
			kind = result.Task.Kind
			r.record(messaging.Outcome{
				Task:          result.Task,
				Err:           result.Err,
				Lag:           result.Lag,
				FailureReason: result.FailureReason,
			})
		case <-ticks:
			kind = ""
		}
	}
}
