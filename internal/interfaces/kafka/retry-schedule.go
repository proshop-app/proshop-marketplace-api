package kafka

import (
	"container/heap"
	"time"
)

// retrySchedule держит один таймер поколения, только пока есть отложенные сообщения.
type retrySchedule struct {
	pending delayedDeliveries
	timer   *time.Timer
	armedAt time.Time
}

type delayedDelivery struct {
	item *delivery
	due  time.Time
}

type delayedDeliveries []delayedDelivery

func (s *retrySchedule) deferRetry(queue *keyQueue, item *delivery, now time.Time) {
	if item.retry.delay <= 0 {
		queue.retry(item)
		return
	}
	heap.Push(&s.pending, delayedDelivery{item: item, due: now.Add(item.retry.delay)})
}

func (s *retrySchedule) wakeup() <-chan time.Time {
	if len(s.pending) == 0 {
		return nil
	}
	due := s.pending[0].due
	if s.timer == nil {
		s.timer = time.NewTimer(time.Until(due))
	} else if !due.Equal(s.armedAt) {
		s.timer.Reset(time.Until(due))
	}
	s.armedAt = due
	return s.timer.C
}

func (s *retrySchedule) release(queue *keyQueue, now time.Time) {
	s.armedAt = time.Time{}
	for len(s.pending) > 0 && !s.pending[0].due.After(now) {
		queue.retry(heap.Pop(&s.pending).(delayedDelivery).item)
	}
}

func (s *retrySchedule) stop() {
	if s.timer != nil {
		s.timer.Stop()
	}
}

func (d delayedDeliveries) Len() int {
	return len(d)
}

func (d delayedDeliveries) Less(i, j int) bool {
	return d[i].due.Before(d[j].due)
}

func (d delayedDeliveries) Swap(i, j int) {
	d[i], d[j] = d[j], d[i]
}

func (d *delayedDeliveries) Push(value any) {
	*d = append(*d, value.(delayedDelivery))
}

func (d *delayedDeliveries) Pop() any {
	last := len(*d) - 1
	value := (*d)[last]
	(*d)[last] = delayedDelivery{}
	*d = (*d)[:last]
	return value
}
