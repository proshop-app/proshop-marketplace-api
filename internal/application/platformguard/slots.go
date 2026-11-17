package platformguard

import (
	"context"
	"errors"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (c *Client) acquire(ctx context.Context, class operationClass) (permit, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.clock.Now()
	if !c.server.available(now) {
		return permit{}, false
	}
	tenant, _ := ctx.Value(tenantContextKey{}).(ids.TenantID)
	state := c.tenantState(tenant, now)
	if tenant != "" && (state == nil || !state.breaker.available(now)) {
		return permit{}, false
	}
	if state == nil {
		state = c.batch
	}
	if c.active[class] >= c.classLimit(class) || state.active[class] >= c.tenantLimit(class) {
		return permit{}, false
	}
	select {
	case c.slots <- struct{}{}:
	default:
		return permit{}, false
	}
	p := permit{server: c.server.start(), tenant: state, class: class}
	if state != nil {
		c.pin(state)
		state.active[class]++
		c.active[class]++
		if state.id != "" {
			p.shop = state.breaker.start()
		}
	}
	return p, true
}

func (c *Client) release(p permit, err error) {
	defer func() { <-c.slots }()
	c.mu.Lock()
	defer c.mu.Unlock()

	var serverFailure *ports.PlatformServerFailure
	shared := errors.As(err, &serverFailure)
	canceled := errors.Is(err, context.Canceled)
	now := c.clock.Now()
	c.server.finish(p.server, shared, canceled, c.cfg, now)
	if p.tenant != nil {
		if p.tenant.id != "" {
			p.tenant.breaker.finish(p.shop, tenantFailure(err), shared || canceled, c.cfg, now)
		}
		p.tenant.active[p.class]--
		c.active[p.class]--
		if p.tenant.inactive() {
			c.unpin(p.tenant)
		}
	}
}
