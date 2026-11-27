package jobs

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// После сигнала даём короткое окно заполнения пачки, в том числе при непрерывном импорте.
const outboxBatchWindow = 125 * time.Millisecond

// OutboxDispatcher обслуживает виды с наступившими сроками.
type OutboxDispatcher interface {
	RunWork(context.Context, []string) ([]messaging.Outcome, []string, bool, error)
	WorkRequest() ports.ClaimRequest
}

// WorkDeadlines читает сроки через пул соответствующего класса.
type WorkDeadlines interface {
	NextWorkByKind(context.Context, ports.ClaimRequest) (map[string]time.Time, error)
}

type outboxCycle struct {
	runner     *OutboxRunner
	dispatcher OutboxDispatcher
	source     WorkDeadlines
	lane       *messaging.IndependentLane
	work       workSchedule
	recoverAt  time.Time
	batchAt    time.Time
}

// RunIndependent останавливает отдельную полосу вместе с основным циклом.
func (r *OutboxRunner) RunIndependent(
	ctx context.Context, lane *messaging.IndependentLane, source WorkDeadlines, watch ports.WorkWatch,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		lane.Wait()
	}()
	cycle := outboxCycle{runner: r, dispatcher: lane, source: source, lane: lane, work: make(workSchedule)}
	return cycle.run(ctx, watch)
}

func (c *outboxCycle) run(ctx context.Context, watch ports.WorkWatch) error {
	c.recoverAll(ctx)
	var changed <-chan struct{}
	if watch != nil {
		changed = watch.Changed()
	}
	for ctx.Err() == nil {
		ticks, stop := workTimer(ctx, c.runner.clock, c.nearest())
		select {
		case <-ctx.Done():
		case <-changed:
			c.onSignal(ctx, watch.Take())
		case <-c.done():
			c.onResult(ctx)
		case <-ticks:
			c.onDue(ctx)
		}
		stop()
	}
	return nil
}

func (c *outboxCycle) nearest() time.Time {
	next := c.recoverAt
	for kind, entry := range c.work {
		if c.available(kind) {
			next = earlierTime(next, laterTime(entry.at, entry.retry.until))
		}
	}
	return next
}

func (c *outboxCycle) available(kind string) bool {
	return c.done() == nil || !slices.Contains(c.lane.DetachedKinds(), kind)
}

func (c *outboxCycle) done() <-chan struct{} {
	if c.lane != nil {
		return c.lane.Done()
	}
	return nil
}

func (c *outboxCycle) onSignal(ctx context.Context, hints ports.WorkHints) {
	_, all := hints[""]
	if all {
		c.recoverAll(ctx)
	}
	var unknown []string
	now := c.runner.clock.Now()
	if !c.batchAt.After(now) {
		c.batchAt = now.Add(outboxBatchWindow)
	}
	for kind, at := range hints {
		if kind == "" {
			continue
		}
		switch {
		case at.IsZero() && c.available(kind):
			if !all {
				delete(c.work, kind)
				unknown = append(unknown, kind)
			}
		case at.After(now):
			c.work.signal(kind, at)
		default:
			c.work.signal(kind, c.batchAt)
		}
	}
	if len(unknown) > 0 {
		c.refresh(ctx, unknown)
		for _, kind := range unknown {
			if entry, ok := c.work[kind]; ok {
				entry.at = laterTime(entry.at, c.batchAt)
				c.work[kind] = entry
			}
		}
	}
}

func (c *outboxCycle) onResult(ctx context.Context) {
	outcomes, err := c.lane.Finish()
	c.runner.record(outcomes)
	c.logError(ctx, err)
	now := c.runner.clock.Now()
	for _, outcome := range outcomes {
		c.work.signal(outcome.Task.Kind, now)
	}
}

func (c *outboxCycle) onDue(ctx context.Context) {
	now := c.runner.clock.Now()
	if !c.recoverAt.After(now) {
		c.recoverAll(ctx)
	}
	kinds := slices.DeleteFunc(c.work.due(now), func(kind string) bool { return !c.available(kind) })
	if len(kinds) == 0 || ctx.Err() != nil {
		return
	}
	outcomes, claimed, full, err := c.dispatcher.RunWork(ctx, kinds)
	c.runner.record(outcomes)
	c.logError(ctx, err)
	if err == nil && !full {
		c.refresh(ctx, kinds)
	}
	c.work.afterAttempt(kinds, claimed, c.runner.clock.Now(), err)
}

func (c *outboxCycle) recoverAll(ctx context.Context) {
	c.recoverAt = c.runner.clock.Now().Add(WorkRecovery)
	c.refresh(ctx, nil)
}

func (c *outboxCycle) refresh(ctx context.Context, kinds []string) {
	request := c.dispatcher.WorkRequest()
	if len(kinds) > 0 {
		kinds = slices.DeleteFunc(slices.Clone(kinds), func(kind string) bool { return !c.available(kind) })
		if len(kinds) == 0 {
			return
		}
		request.Kinds = kinds
	}
	next, err := c.source.NextWorkByKind(ctx, request)
	if err != nil {
		c.logError(ctx, err)
		retryAt := c.runner.clock.Now().Add(time.Second)
		if len(kinds) == 0 {
			c.recoverAt = retryAt
		}
		for _, kind := range kinds {
			c.work.signal(kind, retryAt)
		}
		c.work.delay(kinds, retryAt)
		return
	}
	if len(kinds) == 0 && c.done() != nil {
		for _, kind := range c.lane.DetachedKinds() {
			if entry, ok := c.work[kind]; ok {
				next[kind] = entry.at
			}
		}
	}
	c.work.update(kinds, next)
}

func (c *outboxCycle) logError(ctx context.Context, err error) {
	if err != nil && ctx.Err() == nil {
		c.runner.errors.Inc()
		c.runner.logger.Error("outbox: цикл разбора не удался", slog.Any("error", err))
	}
}
