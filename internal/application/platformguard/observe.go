package platformguard

import (
	"context"
	"log/slog"
	"time"
)

func (c *Client) observe(ctx context.Context, operation, outcome string, started time.Time) {
	if c.metrics != nil {
		c.metrics.PlatformCall(operation, classOf(operation).String(), outcome, time.Since(started))
	}
	if outcome == "ok" || outcome == "stale" || c.logger == nil {
		return
	}
	if c.logAllowed(operation + ":" + outcome) {
		c.logFailure(ctx, operation, outcome)
	}
}

func (c *Client) logAllowed(key string) bool {
	// Идентификаторы покупателей и тексты ошибок не раздувают память журнала.
	now := c.clock.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	last, found := c.logs[key]
	if found && now.Sub(last) < c.cfg.LogInterval {
		return false
	}
	c.logs[key] = now
	return true
}

func (c *Client) logFailure(ctx context.Context, operation, outcome string) {
	c.logger.WarnContext(
		ctx,
		"обращение к серверу магазинов не завершено",
		slog.String("operation", operation),
		slog.String("class", classOf(operation).String()),
		slog.String("reason", outcome),
		slog.Any("tenantId", ctx.Value(tenantContextKey{})),
	)
}
