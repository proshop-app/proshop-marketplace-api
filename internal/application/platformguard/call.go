package platformguard

import (
	"context"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

func call[T any](
	ctx context.Context,
	c *Client,
	class operationClass,
	operation string,
	timeout time.Duration,
	load func(context.Context) (T, error),
) (T, error) {
	started := time.Now()
	budget := callBudget(ctx, timeout)
	p, limit, reason, rejected := c.admit(ctx, class, budget)
	if reason != "" {
		c.observe(ctx, class, operation, reason, started)
		var zero T
		return zero, rejected
	}
	p.strict = timeout >= c.cfg.StrictTimeout
	// Срок, по которому судят магазин, отсчитывается от начала его вызова: ожидание места
	// занято чужой нагрузкой и магазину не засчитывается. Заголовок срока несёт этот остаток.
	callCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	value, err := execute(ctx, callCtx, c, p, load)
	outcome := callOutcome(err)
	if err != nil && ctx.Err() != nil {
		// Исход «timeout» — только истечение срока ограждения, засчитанное магазину.
		outcome = "canceled"
	}
	c.observe(ctx, class, operation, outcome, started)
	return value, callError(err)
}

// callBudget — единственный срок вызова магазина: бюджет ограждения или меньший бюджет,
// переданный вызывающим данными (ports.WithPlatformCallBudget). Его истечение — зависание
// магазина, а не решение вызывающего.
func callBudget(ctx context.Context, timeout time.Duration) time.Duration {
	if budget, ok := ports.PlatformCallBudget(ctx); ok {
		return min(budget, timeout)
	}
	return timeout
}

// admit ждёт место не дольше бюджета вызова и не позже крайнего момента вызывающего; конец ожидания
// и отказ без вызова сервера нейтральны для размыкателей. Отказ размыкателя несёт момент, раньше
// которого повтор отклонят так же. Срок вызова — бюджет, но не дальше крайнего момента.
func (c *Client) admit(
	ctx context.Context,
	class operationClass,
	budget time.Duration,
) (permit, time.Duration, string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, c.untilDeadline(ctx, budget))
	defer cancel()
	p, reason := c.acquire(waitCtx, class)
	switch {
	case reason == "":
		return c.withinDeadline(ctx, p, budget)
	case waitCtx.Err() != nil:
		return p, 0, reason, callError(waitCtx.Err())
	case reason == "server_breaker" || reason == "tenant_breaker":
		rejected := &ports.PlatformRejected{NotBefore: c.reopensAt(ctx, reason), Cause: ErrUnavailable}
		return p, 0, reason, rejected
	default:
		return p, 0, reason, ErrUnavailable
	}
}

// withinDeadline не вызывает сервер, если место получено к крайнему моменту: работа сервера
// заведомо пропала бы, магазин не виноват — место возвращается нейтрально.
func (c *Client) withinDeadline(
	ctx context.Context,
	p permit,
	budget time.Duration,
) (permit, time.Duration, string, error) {
	limit := c.untilDeadline(ctx, budget)
	if limit > 0 {
		return p, limit, "", nil
	}
	c.release(p, context.Canceled)
	return p, 0, "deadline_passed", ErrTimeout.Wrap(context.DeadlineExceeded)
}

// untilDeadline — бюджет, урезанный остатком до крайнего момента вызывающего (часы площадки).
func (c *Client) untilDeadline(ctx context.Context, budget time.Duration) time.Duration {
	if deadline, ok := ports.PlatformCallDeadline(ctx); ok {
		return min(budget, deadline.Sub(c.clock.Now()))
	}
	return budget
}

func execute[T any](
	parent, ctx context.Context,
	c *Client,
	p permit,
	load func(context.Context) (T, error),
) (T, error) {
	value, err := load(ctx)
	if parent.Err() != nil {
		// Вызывающий передумал: отменён запрос покупателя, остановлен процесс, потеряна аренда
		// или истёк его общий бюджет. Отказ магазина это не доказывает — в счёт размыкателей не идёт.
		c.release(p, context.Canceled)
		return value, parent.Err()
	}
	var serverFailure *ports.PlatformServerFailure
	var catalogFailure *ports.PickupCatalogFailure
	catalog := errors.As(err, &catalogFailure)
	shared := errors.As(err, &serverFailure)
	if ctx.Err() != nil && !shared && !catalog {
		// Истёк срок ограждения: магазин не ответил вовремя, это зависание, а не отмена.
		err = ctx.Err()
	}
	c.release(p, err)
	return value, err
}

func callOutcome(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "error"
}

func callError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout.Wrap(err)
	}
	if err != nil && errs.CodeOf(err) == errs.Internal {
		return ErrUnavailable.Wrap(err)
	}
	return err
}
