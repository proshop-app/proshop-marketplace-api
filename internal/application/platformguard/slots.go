package platformguard

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ServerState читает размыкатель без занятия места или запуска пробы.
func (c *Client) ServerState() ports.PlatformServerState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.server.available(c.clock.Now()) {
		return ports.PlatformServerBlocked
	}
	if !c.server.openUntil.IsZero() {
		return ports.PlatformServerProbe
	}
	return ports.PlatformServerReady
}

// CreationCandidates ищет ответивший магазин с другого конца очереди, когда её голова молчит.
func (c *Client) CreationCandidates(probe bool) (bool, []ids.TenantID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	if !probe && !now.Before(c.lastShopTimeout.Add(c.cfg.OpenInterval)) {
		return false, nil
	}
	return true, c.creationExcluded(now)
}

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
	p := permit{server: c.server.start(), tenant: state, class: class, answers: c.answers}
	c.pin(state)
	state.active[class]++
	c.pools[class].active++
	c.running++
	if state.id != "" {
		p.shop = state.breaker.start()
		if p.shop.probe {
			c.updateCreationExclusion(state, c.clock.Now())
		}
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
	now := c.clock.Now()
	v := c.serverVerdict(p, err, now)
	if v == verdictSuccess {
		c.answers++
		// Ответ уже начатого вызова подтверждает, что сервер жив, даже если
		// другой магазин успел открыть общий размыкатель.
		if !c.server.openUntil.IsZero() {
			c.server.completeProbe(false, c.cfg, now)
		}
	}
	c.server.finish(p.server, v, c.cfg, now)
	if p.tenant.id != "" {
		p.tenant.breaker.finish(p.shop, tenantVerdict(err), c.cfg, now)
		c.updateCreationExclusion(p.tenant, now)
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

// reopensAt — когда размыкатель, давший отказ, снова допустит вызов. Пока идёт единственная проба,
// её итог известен не позже строгого срока: до него повтор получит тот же отказ.
func (c *Client) reopensAt(ctx context.Context, reason string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	b := &c.server
	tenant, _ := ctx.Value(tenantContextKey{}).(ids.TenantID)
	if state := c.tenants[tenant]; reason == "tenant_breaker" && state != nil {
		b = &state.breaker
	}
	if b.openUntil.IsZero() {
		return now
	}
	if b.openUntil.After(now) {
		return b.openUntil
	}
	return now.Add(c.cfg.StrictTimeout)
}
