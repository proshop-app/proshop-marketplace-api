// Package fake — реализации портов в памяти для тестов приложения и
// транспорта. Повторяют контракт портов: условные обновления, дедуп,
// откат транзакции при ошибке.
package fake

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Clock — управляемое время.
type Clock struct {
	mu       sync.Mutex
	now      time.Time
	timeouts map[*scheduledContext]context.CancelCauseFunc
}

// Scheduler привязывает сроки контекстов к сдвигам Clock.
type Scheduler struct {
	*Clock
}

type scheduledContext struct {
	context.Context
	deadline time.Time
}

// NewClock — часы, стоящие на now.
func NewClock(now time.Time) *Clock {
	return &Clock{now: now}
}

// Now — текущее время часов.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

// Wait сдвигает часы на d без настоящего ожидания: тест видит паузу по сдвигу времени.
func (c *Clock) Wait(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	c.Advance(d)
	return ctx.Err() == nil
}

// Advance сдвигает часы.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
	c.expireDue()
}

// Scheduler включает управляемые сроки у получателя этих часов.
func (c *Clock) Scheduler() *Scheduler {
	return &Scheduler{Clock: c}
}

func (c *Clock) expireDue() {
	for ctx, cancel := range c.timeouts {
		if !ctx.deadline.After(c.now) {
			cancel(context.DeadlineExceeded)
			delete(c.timeouts, ctx)
		}
	}
}

// WithTimeout создаёт контекст со сроком по тем же часам, что и Now.
func (s *Scheduler) WithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()

	base, cancel := context.WithCancelCause(parent)
	deadline := s.now.Add(d)
	if earlier, ok := parent.Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
	}
	ctx := &scheduledContext{Context: base, deadline: deadline}
	if !deadline.After(s.now) {
		cancel(context.DeadlineExceeded)
	} else {
		if s.timeouts == nil {
			s.timeouts = make(map[*scheduledContext]context.CancelCauseFunc)
		}
		s.timeouts[ctx] = cancel
	}
	return ctx, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.timeouts, ctx)
		cancel(context.Canceled)
	}
}

// Expire доводит часы до срока вызова. Подходит для фейка, который пропускает ответ в срок.
func (s *Scheduler) Expire(ctx context.Context) error {
	at, ok := ctx.Deadline()
	if !ok {
		panic("для имитации таймаута нужен срок контекста")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if at.After(s.now) {
		s.now = at
	}
	s.expireDue()
	return ctx.Err()
}

func (c *scheduledContext) Deadline() (time.Time, bool) {
	return c.deadline, true
}

func (c *scheduledContext) Err() error {
	err := c.Context.Err()
	if err != nil && errors.Is(context.Cause(c.Context), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return err
}
