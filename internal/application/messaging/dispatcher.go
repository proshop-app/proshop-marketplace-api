// Package messaging — служебные сценарии сообщений: разбор outbox, парковка
// отравленных событий и чистка служебных записей по сроку.
package messaging

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// ErrUnknownTask — для вида задачи нет обработчика.
var ErrUnknownTask = errs.New(errs.Internal, "messaging.unknown_task", "нет обработчика задачи")

// MaxBackoff — наибольшая пауза перед повтором задачи.
const MaxBackoff = 10 * time.Minute

// Handler выполняет задачу; ошибка откладывает её на повтор.
type Handler func(ctx context.Context, task ports.Task) error

// BatchHandler возвращает ошибки в порядке задач; nil означает успех всей пачки.
type BatchHandler func(context.Context, []ports.Task) []error

// Outcome — итог одной задачи для журналов и метрик.
type Outcome struct {
	Task ports.Task
	Err  error
	// Lag — сколько задача ждала с постановки до завершения.
	Lag time.Duration
}

// DispatcherConfig — размеры пачки и аренда.
type DispatcherConfig struct {
	Batch     int
	PerTenant int
	Lease     time.Duration
}

// Dispatcher разбирает outbox: берёт созревшие задачи с арендой, выполняет и
// отмечает итог. Задача не теряется: упавший процесс оставляет её до конца аренды.
type Dispatcher struct {
	queue    ports.TaskQueue
	clock    ports.Clock
	handlers map[string]Handler
	batches  map[string]BatchHandler
	config   DispatcherConfig
}

// NewDispatcher — разбор outbox с обработчиками по видам задач.
func NewDispatcher(
	queue ports.TaskQueue,
	clock ports.Clock,
	config DispatcherConfig,
	handlers map[string]Handler,
) *Dispatcher {
	return &Dispatcher{queue: queue, clock: clock, handlers: handlers, config: config}
}

// RegisterBatch подключает групповой обработчик до запуска диспетчера.
func (d *Dispatcher) RegisterBatch(kind string, handler BatchHandler) {
	if d.batches == nil {
		d.batches = map[string]BatchHandler{}
	}
	d.batches[kind] = handler
}

// ReadyCounts возвращает глубину готовой очереди по видам задач.
func (d *Dispatcher) ReadyCounts(ctx context.Context) (map[string]int64, error) {
	counts, err := d.queue.ReadyCounts(ctx, d.clock.Now())
	if err != nil {
		return nil, err
	}
	for kind := range d.handlers {
		if _, ok := counts[kind]; !ok {
			counts[kind] = 0
		}
	}
	for kind := range d.batches {
		if _, ok := counts[kind]; !ok {
			counts[kind] = 0
		}
	}
	return counts, nil
}

// RunOnce разбирает одну справедливо выбранную пачку.
func (d *Dispatcher) RunOnce(ctx context.Context) ([]Outcome, error) {
	now := d.clock.Now()

	tasks, err := d.queue.Claim(ctx, ports.ClaimRequest{
		Now:        now,
		Limit:      d.config.Batch,
		PerTenant:  d.config.PerTenant,
		LeaseUntil: now.Add(d.config.Lease),
	})
	if err != nil {
		return nil, err
	}

	outcomes := make([]Outcome, 0, len(tasks))
	for len(tasks) > 0 {
		if ctx.Err() != nil {
			return outcomes, ctx.Err()
		}
		group := tasks[:1]
		var failures []error
		if handler, ok := d.batches[tasks[0].Kind]; ok {
			group, tasks = takeKind(tasks, tasks[0].Kind)
			failures = handler(ctx, group)
		} else {
			failures = []error{d.run(ctx, tasks[0])}
			tasks = tasks[1:]
		}
		results := make([]Outcome, 0, len(group))
		resolutions := make([]ports.TaskResolution, 0, len(group))
		for n, task := range group {
			finished := d.clock.Now()
			outcome := Outcome{Task: task, Lag: finished.Sub(task.CreatedAt)}
			if len(failures) > 0 {
				outcome.Err = failures[n]
			}
			resolution := ports.TaskResolution{ID: task.ID, Attempts: task.Attempts, At: finished}
			if outcome.Err != nil {
				next := finished.Add(Backoff(task.Attempts))
				resolution.NextAt = &next
				resolution.Error = outcome.Err.Error()
			}
			results = append(results, outcome)
			resolutions = append(resolutions, resolution)
		}
		if err := d.resolve(ctx, resolutions); err != nil {
			return outcomes, err
		}
		outcomes = append(outcomes, results...)
	}
	return outcomes, nil
}

func (d *Dispatcher) resolve(ctx context.Context, results []ports.TaskResolution) error {
	if len(results) != 1 {
		return d.queue.Resolve(ctx, results)
	}
	// Одиночным обработчикам не нужны сериализация пачки и дополнительная транзакция.
	result := results[0]
	if result.NextAt != nil {
		return d.queue.Retry(ctx, result.ID, result.Attempts, *result.NextAt, result.Error)
	}
	return d.queue.Complete(ctx, result.ID, result.Attempts, result.At)
}

func takeKind(tasks []ports.Task, kind string) ([]ports.Task, []ports.Task) {
	group := make([]ports.Task, 0, len(tasks))
	rest := make([]ports.Task, 0, len(tasks))
	for _, task := range tasks {
		if task.Kind == kind {
			group = append(group, task)
		} else {
			rest = append(rest, task)
		}
	}
	return group, rest
}

func (d *Dispatcher) run(ctx context.Context, task ports.Task) error {
	handler, ok := d.handlers[task.Kind]
	if !ok {
		return ErrUnknownTask.WithMessage("нет обработчика задачи " + task.Kind)
	}

	return handler(ctx, task)
}

// Backoff — пауза перед повтором: 1, 2, 4… секунд, не больше MaxBackoff.
func Backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 16 {
		return MaxBackoff
	}

	delay := time.Second << (attempts - 1)
	if delay > MaxBackoff {
		return MaxBackoff
	}

	return delay
}
