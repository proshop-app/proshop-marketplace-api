package checkout

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Ограничения пула и сроков независимого исполнителя оформления.
const (
	// CreationWorkers и CreationPerTenant оставляют свободные места для других магазинов.
	CreationWorkers   = 16
	CreationPerTenant = 2
	// DeadlineWorkers закрывают ожидание независимо от занятых HTTP-вызовами мест.
	DeadlineWorkers     = 4
	CreationCallTimeout = 5 * time.Second
	creationLeaseSlack  = 100 * time.Millisecond
	taskLeaseSlack      = 5 * time.Second
	PollWorkers         = 4
	PollPerTenant       = 1
	CancelWorkers       = 4
	CancelPerTenant     = 1
)

// TaskResult публикуется после отметки задачи; позволяет циклу сразу заполнить свободное место.
type TaskResult struct {
	FailureReason string
	Task          ports.Task
	Err           error
	Lag           time.Duration
}

// Executor разделяет создание, закрытие срока, отмену и сверку на независимые полосы.
// Dispatch вызывается одним циклом, результаты каждого вызова сохраняются независимо.
type Executor struct {
	maxAttempts int
	queue       ports.TaskQueue
	clock       ports.Clock
	service     *Service
	mu          sync.Mutex
	active      map[string]map[ids.TenantID]int
	results     chan TaskResult
	wg          sync.WaitGroup
}

// NewExecutor собирает отдельный пул поверх существующей очереди.
func NewExecutor(queue ports.TaskQueue, clock ports.Clock, service *Service, attempts ...int) *Executor {
	maxAttempts := messaging.DefaultMaxAttempts
	if len(attempts) > 0 && attempts[0] > 0 {
		maxAttempts = attempts[0]
	}
	return &Executor{
		maxAttempts: maxAttempts,
		queue:       queue,
		clock:       clock,
		service:     service,
		active: map[string]map[ids.TenantID]int{
			TaskCreateOrder:    {},
			TaskExpireCreation: {},
			TaskPollOrder:      {},
			TaskCancelOrder:    {},
		},
		results: make(chan TaskResult, CreationWorkers+DeadlineWorkers+PollWorkers+CancelWorkers),
	}
}

// Results сообщает циклу о свободном месте после обработки задачи.
func (e *Executor) Results() <-chan TaskResult { return e.results }

// Wait вызывается после остановки Dispatch и отмены контекста исполнения.
func (e *Executor) Wait() { e.wg.Wait() }

// Dispatch не ждёт HTTP: каждый освободившийся слот доступен следующему Claim.
func (e *Executor) Dispatch(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, kind := range []string{TaskExpireCreation, TaskCreateOrder, TaskCancelOrder, TaskPollOrder} {
		limit, perTenant := CreationWorkers, CreationPerTenant
		switch kind {
		case TaskExpireCreation:
			limit, perTenant = DeadlineWorkers, DeadlineWorkers
		case TaskCancelOrder:
			limit, perTenant = CancelWorkers, CancelPerTenant
		case TaskPollOrder:
			limit, perTenant = PollWorkers, PollPerTenant
		}
		slots := make(map[ids.TenantID]int)
		for tenant, count := range e.active[kind] {
			limit -= count
			slots[tenant] = perTenant - count
		}
		if limit == 0 {
			continue
		}
		now := e.clock.Now()
		tasks, err := e.queue.Claim(ctx, ports.ClaimRequest{
			Now:         now,
			Limit:       limit,
			PerTenant:   perTenant,
			Kinds:       []string{kind},
			TenantSlots: slots,
			LeaseUntil:  now.Add(e.service.orderCallBudget() + taskLeaseSlack),
		})
		if err != nil {
			return err
		}
		for _, task := range tasks {
			e.active[kind][task.TenantID]++
			e.wg.Add(1)
			go e.execute(ctx, task)
		}
	}
	return nil
}

func (e *Executor) execute(ctx context.Context, task ports.Task) {
	defer e.wg.Done()
	err := e.service.HandleOrderTask(ctx, task)
	resolution := messaging.Resolution(task, err, e.clock.Now(), e.maxAttempts)
	if CreationPending(err) {
		resolution.FailureReason = ""
		next := e.clock.Now().Add(checkout.CreationRetryDelay)
		var pending *creationPendingUntil
		if errors.As(err, &pending) {
			next = pending.at
		}
		resolution.NextAt = &next
	}
	var saved error
	if ctx.Err() != nil {
		// Не записываем отказ при SIGTERM: задача остаётся арендованной до повторного Claim.
		saved = ctx.Err()
	} else {
		saved = e.queue.Resolve(ctx, []ports.TaskResolution{resolution})
	}
	reason := resolution.FailureReason
	e.mu.Lock()
	e.active[task.Kind][task.TenantID]--
	if e.active[task.Kind][task.TenantID] == 0 {
		delete(e.active[task.Kind], task.TenantID)
	}
	e.mu.Unlock()
	if saved != nil {
		err = saved
		reason = ""
	}
	select {
	case e.results <- TaskResult{
		Task:          task,
		Err:           err,
		Lag:           e.clock.Now().Sub(task.CreatedAt),
		FailureReason: reason,
	}:
	case <-ctx.Done():
	}
}
