package checkout

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/safego"
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

// NewExecutor собирает отдельный пул поверх существующей очереди; maxAttempts — предел неизвестных сбоев.
func NewExecutor(queue ports.TaskQueue, clock ports.Clock, service *Service, maxAttempts int) *Executor {
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
			safego.Go("checkout.executor", func() { e.execute(ctx, task) })
		}
	}
	return nil
}

func (e *Executor) execute(ctx context.Context, task ports.Task) {
	defer e.wg.Done()
	// Слот магазина освобождается и при панике вне обработчика (отметка итога):
	// иначе магазин навсегда потерял бы место в полосе оформления.
	released := false
	defer func() {
		if !released {
			e.release(task)
		}
	}()
	// Паника обработчика — обычная ошибка задачи: повтор или карантин.
	err := errs.Guard(func() error { return e.service.HandleOrderTask(ctx, task) })
	resolution := e.resolution(task, err)
	saved := e.save(ctx, resolution)
	reason := resolution.FailureReason
	e.release(task)
	released = true
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

func (e *Executor) release(task ports.Task) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active[task.Kind][task.TenantID]--
	if e.active[task.Kind][task.TenantID] == 0 {
		delete(e.active[task.Kind], task.TenantID)
	}
}

// resolution — итог задачи; ожидание подтверждения магазина — отложенный повтор, не сбой.
func (e *Executor) resolution(task ports.Task, err error) ports.TaskResolution {
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
	return resolution
}

// save не записывает отказ при SIGTERM: задача остаётся арендованной до повторного Claim.
func (e *Executor) save(ctx context.Context, resolution ports.TaskResolution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return e.queue.Resolve(ctx, []ports.TaskResolution{resolution})
}
