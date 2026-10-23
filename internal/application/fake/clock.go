// Package fake — реализации портов в памяти для тестов приложения и
// транспорта. Повторяют контракт портов: условные обновления, дедуп,
// откат транзакции при ошибке.
package fake

import (
	"sync"
	"time"
)

// Clock — управляемое время.
type Clock struct {
	mu  sync.Mutex
	now time.Time
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

// Advance сдвигает часы.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}
