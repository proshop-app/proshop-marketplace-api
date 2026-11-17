package platformguard

import (
	"container/list"
	"context"
	"errors"
)

type slotPool struct {
	active  int
	queued  int
	ready   list.List
	waiting list.List
}

type admission struct {
	permit permit
	reason string
}

type waiter struct {
	ctx      context.Context
	state    *tenantState
	class    operationClass
	position *list.Element
	all      *list.Element
	done     chan admission
}

func (c *Client) enqueue(ctx context.Context, state *tenantState, class operationClass) (admission, *waiter) {
	pool := &c.pools[class]
	if pool.queued >= c.cfg.QueueSize || state.queued[class].Len() >= c.cfg.TenantQueueSize {
		return admission{reason: "queue_full"}, nil
	}
	w := &waiter{ctx: ctx, state: state, class: class, done: make(chan admission, 1)}
	c.pin(state)
	w.position = state.queued[class].PushBack(w)
	w.all = pool.waiting.PushBack(w)
	pool.queued++
	c.markReady(state, class)
	c.dispatch(class)
	return admission{}, w
}

func (c *Client) await(ctx context.Context, w *waiter) admission {
	select {
	case result := <-w.done:
		return result
	case <-ctx.Done():
		return c.cancelWaiter(ctx, w)
	}
}

func (c *Client) cancelWaiter(ctx context.Context, w *waiter) admission {
	result := admission{reason: waitingOutcome(ctx)}
	c.mu.Lock()
	if w.position != nil {
		c.resolve(w, result)
		c.dispatch(w.class)
		c.mu.Unlock()
		return <-w.done
	}
	c.mu.Unlock()
	// Допуск и отмена могли совпасть: уже выданное место возвращается без вызова сервера.
	granted := <-w.done
	if granted.reason == "" {
		c.release(granted.permit, context.Canceled)
	}
	return result
}

func waitingOutcome(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "queue_timeout"
	}
	return "canceled"
}

func (c *Client) markReady(state *tenantState, class operationClass) {
	if state.ready[class] != nil {
		c.pools[class].ready.Remove(state.ready[class])
		state.ready[class] = nil
	}
	if state.queued[class].Len() > 0 && state.active[class] < c.tenantLimit(class) {
		state.ready[class] = c.pools[class].ready.PushBack(state)
	}
}

func (c *Client) dispatch(class operationClass) {
	pool := &c.pools[class]
	for pool.ready.Len() > 0 && pool.active < c.classLimit(class) && c.running < c.cfg.MaxConcurrent {
		state := pool.ready.Front().Value.(*tenantState)
		w := state.queued[class].Front().Value.(*waiter)
		reason := c.breakerReason(state)
		if w.ctx.Err() != nil {
			reason = waitingOutcome(w.ctx)
		}
		result := admission{reason: reason}
		if reason == "" {
			result.permit = c.start(state, class)
		}
		c.resolve(w, result)
	}
}

func (c *Client) resolve(w *waiter, result admission) {
	w.state.queued[w.class].Remove(w.position)
	w.position = nil
	pool := &c.pools[w.class]
	pool.waiting.Remove(w.all)
	pool.queued--
	c.markReady(w.state, w.class)
	if w.state.inactive() {
		c.unpin(w.state)
	}
	w.done <- result
}

func (c *Client) rejectBlocked(state *tenantState) {
	if !c.server.available(c.clock.Now()) {
		for class := checkoutClass; class < classCount; class++ {
			c.rejectWaiting(&c.pools[class].waiting, "server_breaker")
		}
	} else if reason := c.breakerReason(state); reason != "" {
		for class := checkoutClass; class < classCount; class++ {
			c.rejectWaiting(&state.queued[class], reason)
		}
	}
}

func (c *Client) rejectWaiting(queue *list.List, reason string) {
	for queue.Len() > 0 {
		c.resolve(queue.Front().Value.(*waiter), admission{reason: reason})
	}
}
