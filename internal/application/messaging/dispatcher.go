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

// RunOnce разбирает одну пачку. Пачка неполная — очередь пуста.
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

	for _, task := range tasks {
		if ctx.Err() != nil {
			return outcomes, ctx.Err()
		}

		outcome := Outcome{Task: task, Err: d.run(ctx, task)}
		finished := d.clock.Now()
		outcome.Lag = finished.Sub(task.CreatedAt)

		if outcome.Err == nil {
			err = d.queue.Complete(ctx, task.ID, finished)
		} else {
			err = d.queue.Retry(ctx, task.ID, finished.Add(Backoff(task.Attempts)), outcome.Err.Error())
		}
		if err != nil {
			return outcomes, err
		}

		outcomes = append(outcomes, outcome)
	}

	return outcomes, nil
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
