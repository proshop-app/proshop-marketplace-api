// Package messaging — служебные сценарии сообщений: разбор outbox, парковка
// отравленных событий и чистка служебных записей по сроку.
package messaging

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/safego"
)

// ErrInvalidTask — сохранённое намерение нельзя исполнить с теми же данными.
var ErrInvalidTask = errs.New(errs.Invalid, "messaging.invalid_task", "некорректная задача повтора")

// ErrUnknownTask — для вида задачи нет обработчика.
var ErrUnknownTask = errs.New(errs.Invalid, "messaging.unknown_task", "нет обработчика задачи")

// MaxBackoff — наибольшая пауза перед повтором задачи.
const MaxBackoff = 10 * time.Minute

// DefaultMaxAttempts ограничивает только ошибки без установленной причины.
const DefaultMaxAttempts = 10

// Handler выполняет задачу; класс ошибки определяет повтор или карантин.
type Handler func(ctx context.Context, task ports.Task) error

// BatchHandler возвращает ошибки в порядке задач; nil означает успех всей пачки.
type BatchHandler func(context.Context, []ports.Task) []error

// Outcome — итог одной задачи для журналов и метрик.
type Outcome struct {
	Task          ports.Task
	FailureReason string
	Err           error
	// Lag — сколько задача ждала с постановки до завершения.
	Lag time.Duration
}

// DispatcherConfig — размеры пачки и аренда.
type DispatcherConfig struct {
	MaxAttempts int
	// Kinds ограничивает выборку точечных задач выделенного пула.
	Kinds          []string
	ExcludeKinds   []string
	OrderedKinds   []string
	Batch          int
	PerTenant      int
	Lease          time.Duration
	Concurrency    int
	HandlerTimeout time.Duration
}

// Dispatcher разбирает outbox: берёт созревшие задачи с арендой, выполняет и
// отмечает итог. Задача не теряется: упавший процесс оставляет её до конца аренды.
type Dispatcher struct {
	claimMu        sync.Mutex
	singleAfter    ports.ClaimCursor
	batchAfter     ports.ClaimCursor
	batchKindAfter int
	queue          ports.TaskQueue
	clock          ports.Clock
	handlers       map[string]Handler
	batches        map[string]BatchHandler
	config         DispatcherConfig
}

// NewDispatcher — разбор outbox с обработчиками по видам задач.
func NewDispatcher(
	queue ports.TaskQueue,
	clock ports.Clock,
	config DispatcherConfig,
	handlers map[string]Handler,
) *Dispatcher {
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = DefaultMaxAttempts
	}
	if config.Batch <= 0 {
		config.Batch = 200
	}
	if config.PerTenant <= 0 {
		config.PerTenant = config.Batch
	}
	if config.Lease <= 0 {
		config.Lease = time.Minute
	}
	if config.Concurrency <= 0 {
		config.Concurrency = 8
	}
	// Один слот всегда остаётся для точечных задач рядом с пакетным обработчиком.
	config.Concurrency = max(2, config.Concurrency)
	// Обработчик укладывается в половину аренды: вторая половина — отметка итога прохода.
	// Процесс задаёт срок сам и проверяет аренду на старте, урезание здесь не срабатывает.
	if config.HandlerTimeout <= 0 || config.HandlerTimeout > config.Lease/2 {
		config.HandlerTimeout = config.Lease / 2
	}
	return &Dispatcher{queue: queue, clock: clock, handlers: handlers, config: config}
}

// RegisterBatch подключает групповой обработчик до запуска диспетчера.
func (d *Dispatcher) RegisterBatch(kind string, handler BatchHandler) {
	if d.batches == nil {
		d.batches = map[string]BatchHandler{}
	}
	d.batches[kind] = handler
}

// ReadyLanes возвращает созревшую работу по видам; вид с обработчиком без работы — нулевая полоса,
// чтобы метрика сбрасывалась.
func (d *Dispatcher) ReadyLanes(ctx context.Context) ([]ports.ReadyLane, error) {
	now := d.clock.Now()
	lanes, err := d.queue.ReadyLanes(ctx, now)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, lane := range lanes {
		seen[lane.Kind] = true
	}
	for _, kinds := range []map[string]bool{handlerKinds(d.handlers), handlerKinds(d.batches)} {
		for kind := range kinds {
			if !seen[kind] {
				seen[kind] = true
				lanes = append(lanes, ports.ReadyLane{Kind: kind, Oldest: now})
			}
		}
	}
	return lanes, nil
}

// RunOnce разбирает одну справедливо выбранную пачку.
func (d *Dispatcher) RunOnce(ctx context.Context) ([]Outcome, error) {
	tasks, err := d.claimPass(ctx, ports.TaskLaneGate{}, nil)
	if err != nil {
		return nil, err
	}
	return d.executePass(ctx, tasks, nil)
}

// Пакетный обработчик получает одну порцию, а не N последовательных вызовов.
// Точечный выбор сначала даёт слот каждому магазину, затем заполняет свободные слоты.
func (d *Dispatcher) claimPass(
	ctx context.Context,
	gate ports.TaskLaneGate,
	exclude []string,
) ([]ports.Task, error) {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	now := d.clock.Now()
	request := ports.ClaimRequest{
		Gate:         gate,
		Now:          now,
		ExcludeKinds: slices.Concat(d.config.ExcludeKinds, exclude),
		Limit:        d.config.Batch,
		PerTenant:    d.config.PerTenant,
		LeaseUntil:   now.Add(d.config.Lease),
	}
	kinds := []string{}
	for kind := range d.batches {
		if !slices.Contains(request.ExcludeKinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	slices.Sort(kinds)
	allBatchKinds := slices.Clone(kinds)
	if len(kinds) > 0 {
		start := d.batchKindAfter % len(kinds)
		kinds = append(slices.Clone(kinds[start:]), kinds[:start]...)
		kinds = kinds[:min(len(kinds), d.config.Concurrency-1)]
		d.batchKindAfter = (start + len(kinds)) % len(allBatchKinds)
	}
	tasks := []ports.Task{}
	if len(kinds) > 0 {
		request.Kinds = kinds
		request.After = d.batchAfter
		claimed, err := d.queue.Claim(ctx, request)
		if err != nil {
			return nil, err
		}
		// Курсор двигается и после пустого взятия: обход полос ограничен, следующий продолжит дальше.
		tasks, d.batchAfter = claimed.Tasks, claimed.Next
	}
	batchKinds := map[string]bool{}
	for _, task := range tasks {
		batchKinds[task.Kind] = true
	}
	request.Kinds = d.config.Kinds
	request.After = d.singleAfter
	request.ExcludeKinds = append(request.ExcludeKinds, allBatchKinds...)
	request.OrderedKinds = d.config.OrderedKinds
	request.PerTenant = min(d.config.PerTenant, d.config.Concurrency)
	request.Limit = min(d.config.Batch, d.config.Concurrency-len(batchKinds))
	if request.Limit == 0 {
		return tasks, nil
	}
	singles, err := d.queue.Claim(ctx, request)
	if err != nil {
		return tasks, err
	}
	d.singleAfter = singles.Next
	return append(tasks, singles.Tasks...), nil
}

// executePass не держит локальный хвост одного магазина. Все взятые порции
// сразу получают слот; новая задача другого магазина попадёт в следующий проход.
func (d *Dispatcher) executePass(
	ctx context.Context,
	tasks []ports.Task,
	lane *IndependentLane,
) ([]Outcome, error) {
	groups := [][]ports.Task{}
	for len(tasks) > 0 {
		group := tasks[:1]
		if _, ok := d.batches[tasks[0].Kind]; ok {
			group, tasks = takeKind(tasks, tasks[0].Kind)
		} else {
			tasks = tasks[1:]
		}
		groups = append(groups, group)
	}
	results := make([][]Outcome, len(groups))
	resolutions := make([][]ports.TaskResolution, len(groups))
	failures := make([]error, len(groups))
	var workers sync.WaitGroup
	for n, group := range groups {
		workers.Add(1)
		safego.Run(
			"outbox.group",
			func() error {
				var err error
				results[n], resolutions[n], err = d.executeGroup(ctx, group)
				return err
			},
			func(err error) {
				// Паника вне обработчика срывает проход: задачи дождутся аренды и повтора.
				failures[n] = err
				workers.Done()
			},
		)
	}
	workers.Wait()
	outcomes := []Outcome{}
	allResolutions := []ports.TaskResolution{}
	for n, result := range results {
		if failures[n] != nil {
			return outcomes, failures[n]
		}
		outcomes = append(outcomes, result...)
		allResolutions = append(allResolutions, resolutions[n]...)
	}
	if lane != nil {
		lane.pauseOnFailure(ctx, outcomes, allResolutions)
	}
	// Ограниченный проход подтверждается одним запросом, без N отдельных COMMIT.
	if err := d.resolve(ctx, allResolutions); err != nil {
		return nil, err
	}
	return outcomes, ctx.Err()
}

func (d *Dispatcher) executeGroup(
	ctx context.Context,
	group []ports.Task,
) ([]Outcome, []ports.TaskResolution, error) {
	handling, cancel := context.WithTimeout(ctx, d.config.HandlerTimeout)
	defer cancel()
	failures := d.handle(handling, group)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	results := make([]Outcome, 0, len(group))
	resolutions := make([]ports.TaskResolution, 0, len(group))
	for n, task := range group {
		finished := d.clock.Now()
		outcome := Outcome{Task: task, Lag: finished.Sub(task.CreatedAt)}
		if handling.Err() != nil {
			outcome.Err = handling.Err()
		} else if len(failures) > 0 {
			if len(failures) != len(group) {
				outcome.Err = errs.New(errs.Internal, "messaging.batch_result", "неполный результат пачки")
			} else {
				outcome.Err = failures[n]
			}
		}
		resolution := Resolution(task, outcome.Err, finished, d.config.MaxAttempts)
		outcome.FailureReason = resolution.FailureReason
		results = append(results, outcome)
		resolutions = append(resolutions, resolution)
	}
	return results, resolutions, nil
}

func (d *Dispatcher) resolve(ctx context.Context, results []ports.TaskResolution) error {
	if len(results) == 0 {
		return nil
	}
	if len(results) != 1 {
		return d.queue.Resolve(ctx, results)
	}
	result := results[0]
	if result.FailureReason != "" || result.UnknownFailures > 0 || result.PauseKey != "" {
		return d.queue.Resolve(ctx, results)
	}
	// Одиночным обработчикам не нужны сериализация пачки и дополнительная транзакция.
	if result.NextAt != nil {
		return d.queue.Retry(ctx, result.ID, result.Attempts, *result.NextAt, result.Error)
	}
	return d.queue.Complete(ctx, result.ID, result.Attempts, result.At)
}

func handlerKinds[H any](handlers map[string]H) map[string]bool {
	kinds := make(map[string]bool, len(handlers))
	for kind := range handlers {
		kinds[kind] = true
	}
	return kinds
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

// handle — единственное место перехвата паники обработчиков outbox: паника
// становится ошибкой internal каждой задачи группы и идёт обычным путём повтора
// и карантина, а соседние группы прохода и процесс продолжают работу.
func (d *Dispatcher) handle(ctx context.Context, group []ports.Task) []error {
	batch, ok := d.batches[group[0].Kind]
	if !ok {
		return []error{errs.Guard(func() error { return d.run(ctx, group[0]) })}
	}
	var failures []error
	if err := errs.Guard(func() error {
		failures = batch(ctx, group)
		return nil
	}); err != nil {
		return slices.Repeat([]error{err}, len(group))
	}
	return failures
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

// FailureReason отделяет постоянный отказ и исчерпание попыток от повторяемого сбоя.
func FailureReason(err error, unknownFailures, maxAttempts int) string {
	if err == nil || errs.RetryKindOf(err) == errs.RetryTransient {
		return ""
	}
	if errs.RetryKindOf(err) == errs.RetryPermanent {
		typed, _ := errs.As(err)
		return typed.Key()
	}
	if unknownFailures >= maxAttempts {
		return "attempts_exhausted"
	}
	return ""
}

// Resolution считает неизвестные сбои отдельно от токена аренды и сетевых повторов.
func Resolution(task ports.Task, err error, at time.Time, maxAttempts int) ports.TaskResolution {
	result := ports.TaskResolution{
		ID:              task.ID,
		Attempts:        task.Attempts,
		UnknownFailures: task.UnknownFailures,
		At:              at,
	}
	if err == nil {
		return result
	}
	if errs.RetryKindOf(err) == errs.RetryUnknown {
		result.UnknownFailures++
	}
	result.Error = err.Error()
	result.FailureReason = FailureReason(err, result.UnknownFailures, maxAttempts)
	if result.FailureReason == "" {
		next := at.Add(Backoff(task.Attempts))
		result.NextAt = &next
	}
	return result
}

// Failures читает карантин по курсору без COUNT и обхода живой очереди.
func (d *Dispatcher) Failures(ctx context.Context, after int64, limit int) ([]ports.TaskFailure, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return nil, errs.New(errs.Invalid, "messaging.failure_filter", "некорректная страница карантина")
	}
	return d.queue.Failures(ctx, after, limit)
}

// RetryStats читает один диагностический срез по отдельному расписанию.
func (d *Dispatcher) RetryStats(ctx context.Context) ([]ports.TaskRetryStats, error) {
	return d.queue.RetryStats(ctx)
}
