package jobs

import (
	"context"
	"log/slog"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// creationRecovery — страховочная сверка оформления. Короче срока подтверждения покупки
// (appcheckout.CreationTimeout, 15 с): покупка, чей сигнал потерян вместе с упавшей репликой,
// успевает уйти в магазин с соседней, а не истекает в ожидании общей сверки в 30 с.
const creationRecovery = 5 * time.Second

// CreationRunner пополняет пул после каждого результата, независимо от других задач outbox.
type CreationRunner struct {
	executor *appcheckout.Executor
	logger   *slog.Logger
	clock    ports.Clock
	record   func(messaging.Outcome)
}

type creationCycle struct {
	runner    *CreationRunner
	source    WorkDeadlines
	kinds     []string
	work      workSchedule
	recoverAt time.Time
}

// NewCreationRunner подключает пул к жизненному циклу процесса.
func NewCreationRunner(
	executor *appcheckout.Executor,
	logger *slog.Logger,
	record func(messaging.Outcome),
	clock ports.Clock,
) *CreationRunner {
	return &CreationRunner{executor: executor, logger: logger, clock: clock, record: record}
}

// Run следит за каждой полосой отдельно и сразу пополняет освободившееся место.
func (r *CreationRunner) Run(ctx context.Context, source WorkDeadlines, watch ports.WorkWatch) error {
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		r.executor.Wait()
	}()
	cycle := creationCycle{runner: r, source: source, kinds: r.executor.WorkKinds(), work: make(workSchedule)}
	cycle.recoverAll(ctx)
	for ctx.Err() == nil {
		timer, stop := workTimer(ctx, r.clock, cycle.nearest())
		select {
		case <-ctx.Done():
		case <-watch.Changed():
			cycle.onSignal(ctx, watch.Take())
		case result := <-r.executor.Results():
			cycle.onResult(result)
			for pending := len(r.executor.Results()); pending > 0; pending-- {
				cycle.onResult(<-r.executor.Results())
			}
			cycle.onDue(ctx)
		case <-timer:
			cycle.onDue(ctx)
		}
		stop()
	}
	return nil
}

func (c *creationCycle) nearest() time.Time {
	return earlierTime(c.recoverAt, c.work.nearest())
}

func (c *creationCycle) onSignal(ctx context.Context, hints ports.WorkHints) {
	if _, all := hints[""]; all {
		clear(c.work)
		c.recoverAll(ctx)
		return
	}
	var unknown []string
	for kind, at := range hints {
		if at.IsZero() {
			delete(c.work, kind)
			unknown = append(unknown, kind)
		} else {
			c.work.signal(kind, at)
		}
	}
	if len(unknown) > 0 {
		c.refresh(ctx, ports.ClaimRequest{Kinds: unknown})
	}
}

func (c *creationCycle) onResult(result appcheckout.TaskResult) {
	c.runner.record(messaging.Outcome{
		Task:          result.Task,
		Err:           result.Err,
		Lag:           result.Lag,
		FailureReason: result.FailureReason,
	})
	c.work.signal(result.Task.Kind, c.runner.clock.Now())
}

func (c *creationCycle) onDue(ctx context.Context) {
	now := c.runner.clock.Now()
	if !c.recoverAt.After(now) {
		c.recoverAll(ctx)
	}
	// Порядок полос оставляет закрытие срока впереди вызовов сервера.
	for _, kind := range c.kinds {
		entry := c.work[kind]
		if ctx.Err() != nil || !isDue(laterTime(entry.at, entry.retry.until), now) {
			continue
		}
		request, notBefore := c.runner.executor.WorkRequest(kind)
		switch {
		case request.Limit == 0:
			delete(c.work, kind)
		case notBefore.After(now):
			entry.at = notBefore
			c.work[kind] = entry
		default:
			claimed, full, err := c.runner.executor.Refill(ctx, kind)
			switch {
			case err != nil:
				c.logError(ctx, err)
			case full:
				delete(c.work, kind)
			default:
				request, _ = c.runner.executor.WorkRequest(kind)
				c.refresh(ctx, request)
			}
			if entry, ok := c.work[kind]; ok {
				entry.retry.afterAttempt(c.runner.clock.Now(), claimed > 0, err)
				c.work[kind] = entry
			}
		}
	}
}

func (c *creationCycle) recoverAll(ctx context.Context) {
	c.recoverAt = c.runner.clock.Now().Add(creationRecovery)
	c.refresh(ctx, ports.ClaimRequest{Kinds: c.kinds})
}

func (c *creationCycle) refresh(ctx context.Context, request ports.ClaimRequest) {
	next, err := c.source.NextWorkByKind(ctx, request)
	if err != nil {
		c.logError(ctx, err)
		retryAt := c.runner.clock.Now().Add(time.Second)
		for _, kind := range request.Kinds {
			c.work.signal(kind, retryAt)
		}
		c.work.delay(request.Kinds, retryAt)
		return
	}
	c.work.update(request.Kinds, next)
	for kind, at := range next {
		_, notBefore := c.runner.executor.WorkRequest(kind)
		entry := c.work[kind]
		entry.at = laterTime(at, notBefore)
		c.work[kind] = entry
	}
}

func (c *creationCycle) logError(ctx context.Context, err error) {
	if ctx.Err() == nil {
		c.runner.logger.Error("создание: очередь недоступна", slog.Any("error", err))
	}
}
