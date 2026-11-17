package platformguard

import (
	"container/list"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type tenantState struct {
	id       ids.TenantID
	breaker  breaker
	active   [classCount]int
	queued   [classCount]list.List
	ready    [classCount]*list.Element
	idle     *list.Element
	lastUsed time.Time
}

// Состояния с текущими вызовами закреплены; неактивные вытесняются за O(1), без обхода магазинов.
func (c *Client) tenantState(id ids.TenantID, now time.Time) *tenantState {
	if id == "" {
		return nil
	}
	state := c.tenants[id]
	if state != nil && state.idle != nil && now.Sub(state.lastUsed) >= c.cfg.TenantIdleTTL {
		c.removeTenant(state)
		state = nil
	}
	if state != nil {
		return state
	}
	if len(c.tenants) >= c.cfg.TenantStateSize {
		oldest := c.idle.Front()
		if oldest == nil {
			return nil
		}
		c.removeTenant(oldest.Value.(*tenantState))
	}
	state = &tenantState{id: id, lastUsed: now}
	state.idle = c.idle.PushBack(state)
	c.tenants[id] = state
	return state
}

func (c *Client) removeTenant(state *tenantState) {
	c.idle.Remove(state.idle)
	delete(c.tenants, state.id)
}

func (c *Client) pin(state *tenantState) {
	if state.idle != nil {
		c.idle.Remove(state.idle)
		state.idle = nil
	}
}

func (c *Client) unpin(state *tenantState) {
	if state.id == "" || state.idle != nil {
		return
	}
	state.lastUsed = c.clock.Now()
	state.idle = c.idle.PushBack(state)
}

func (s *tenantState) inactive() bool {
	return s.active[checkoutClass] == 0 && s.active[readClass] == 0 &&
		s.queued[checkoutClass].Len() == 0 && s.queued[readClass].Len() == 0
}
