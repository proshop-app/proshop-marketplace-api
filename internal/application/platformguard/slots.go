package platformguard

import (
	"context"
	"errors"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (c *Client) acquire(ctx context.Context, class operationClass) (permit, string) {
	if ctx.Err() != nil {
		return permit{}, waitingOutcome(ctx)
	}
	result, waiter := c.request(ctx, class)
	if waiter != nil {
		result = c.await(ctx, waiter)
	}
	if result.reason == "" && ctx.Err() != nil {
		c.release(result.permit, context.Canceled)
		return permit{}, waitingOutcome(ctx)
	}
	return result.permit, result.reason
}

func (c *Client) request(ctx context.Context, class operationClass) (admission, *waiter) {
	c.mu.Lock()
	defer c.mu.Unlock()

	tenant, _ := ctx.Value(tenantContextKey{}).(ids.TenantID)
	state := c.tenantState(tenant, c.clock.Now())
	if tenant != "" && state == nil {
		return admission{reason: "queue_full"}, nil
	}
	if state == nil {
		state = c.batch
	}
	if reason := c.breakerReason(state); reason != "" {
		return admission{reason: reason}, nil
	}
	if c.hasSlot(state, class) && c.pools[class].ready.Len() == 0 {
		return admission{permit: c.start(state, class)}, nil
	}
	return c.enqueue(ctx, state, class)
}

func (c *Client) hasSlot(state *tenantState, class operationClass) bool {
	return c.running < c.cfg.MaxConcurrent && c.pools[class].active < c.classLimit(class) &&
		state.active[class] < c.tenantLimit(class)
}

func (c *Client) start(state *tenantState, class operationClass) permit {
	p := permit{server: c.server.start(), tenant: state, class: class}
	c.pin(state)
	state.active[class]++
	c.pools[class].active++
	c.running++
	if state.id != "" {
		p.shop = state.breaker.start()
	}
	return p
}

func (c *Client) release(p permit, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.finishBreakers(p, err)
	p.tenant.active[p.class]--
	c.pools[p.class].active--
	c.running--
	c.rejectBlocked(p.tenant)
	c.markReady(p.tenant, p.class)
	if p.tenant.inactive() {
		c.unpin(p.tenant)
	}
	for class := checkoutClass; class < classCount; class++ {
		c.dispatch(class)
	}
}

func (c *Client) finishBreakers(p permit, err error) {
	var serverFailure *ports.PlatformServerFailure
	shared := errors.As(err, &serverFailure)
	var catalogFailure *ports.PickupCatalogFailure
	catalog := errors.As(err, &catalogFailure)
	canceled := errors.Is(err, context.Canceled)
	now := c.clock.Now()
	serverNeutral := canceled || (!shared && errors.Is(err, context.DeadlineExceeded))
	c.server.finish(p.server, shared, serverNeutral, c.cfg, now)
	if p.tenant.id != "" {
		p.tenant.breaker.finish(p.shop, tenantFailure(err), shared || canceled || catalog, c.cfg, now)
	}
}

func (c *Client) breakerReason(state *tenantState) string {
	now := c.clock.Now()
	if !c.server.available(now) {
		return "server_breaker"
	}
	if state.id != "" && !state.breaker.available(now) {
		return "tenant_breaker"
	}
	return ""
}
