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
	operation string,
	timeout time.Duration,
	load func(context.Context) (T, error),
) (T, error) {
	started := time.Now()
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	p, reason := c.acquire(callCtx, classOf(operation))
	if reason != "" {
		c.observe(ctx, operation, reason, started)
		var zero T
		if callCtx.Err() != nil {
			return zero, callError(callCtx.Err())
		}
		return zero, ErrUnavailable
	}
	value, err := execute(ctx, callCtx, c, p, load)
	c.observe(ctx, operation, callOutcome(err), started)
	return value, callError(err)
}

func execute[T any](
	parent, ctx context.Context,
	c *Client,
	p permit,
	load func(context.Context) (T, error),
) (T, error) {
	value, err := load(ctx)
	var serverFailure *ports.PlatformServerFailure
	shared := errors.As(err, &serverFailure)
	if ctx.Err() != nil && (!shared || errors.Is(ctx.Err(), context.Canceled)) {
		err = ctx.Err()
	}
	if parent.Err() != nil {
		// Более короткий бюджет вызывающего кода не доказывает отказ магазина или общего сервера.
		c.release(p, context.Canceled)
		return value, parent.Err()
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
