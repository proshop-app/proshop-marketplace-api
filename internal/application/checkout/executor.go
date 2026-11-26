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
	// taskStoreBudget — записи обработчика вне вызова магазина под его сроком: захват группы,
	// итог, возврат в корзину, повторы конфликта версии.
	taskStoreBudget = 5 * time.Second
	// Аренда от начала взятия покрывает взятие, обработчик, сохранение ответа без отмены
	// и отметку задачи (5 + 15 + 3 + 5 секунд в худшем случае).
	taskLease       = 2 * (2*CreationCallTimeout + taskStoreBudget)
	PollWorkers     = 4
	PollPerTenant   = 1
	CancelWorkers   = 4
	CancelPerTenant = 1
	// ExecutorSlots — все места полос; каждое держит не больше одного соединения
	// базы за раз: транзакции короткие, вызов магазина идёт вне транзакции.
	ExecutorSlots = CreationWorkers + DeadlineWorkers + PollWorkers + CancelWorkers
)

// TaskResult публикуется после отметки задачи; позволяет циклу сразу заполнить свободное место.
type TaskResult struct {
	FailureReason string
	Task          ports.Task
	Err           error
	Lag           time.Duration
}

type executorLane struct {
	kind      string
	workers   int
	perTenant int
}

var executorLanes = [...]executorLane{
	{TaskExpireCreation, DeadlineWorkers, DeadlineWorkers},
	{TaskCancelOrder, CancelWorkers, CancelPerTenant},
	{TaskPollOrder, PollWorkers, PollPerTenant},
	{TaskCreateOrder, CreationWorkers, CreationPerTenant},
}

// Executor разделяет создание, закрытие срока, отмену и сверку на независимые полосы.
// Dispatch вызывается одним циклом, результаты каждого вызова сохраняются независимо.
type Executor struct {
	maxAttempts int
	queue       ports.TaskQueue
	clock       ports.Clock
	service     *Service
	gate        ports.PlatformServerGate
	mu          sync.Mutex
	active      map[string]map[ids.TenantID]int
	// after — курсор круга магазинов по виду: каждое взятие продолжает с прошлого.
	after   map[string]ports.ClaimCursor
	results chan TaskResult
	wg      sync.WaitGroup
}

// NewExecutor собирает отдельный пул поверх существующей очереди; maxAttempts — предел неизвестных сбоев.
func NewExecutor(
	queue ports.TaskQueue,
	clock ports.Clock,
	service *Service,
	gate ports.PlatformServerGate,
	maxAttempts int,
) *Executor {
	return &Executor{
		maxAttempts: maxAttempts,
		queue:       queue,
		clock:       clock,
		service:     service,
		gate:        gate,
		active: map[string]map[ids.TenantID]int{
			TaskCreateOrder:    {},
			TaskExpireCreation: {},
			TaskPollOrder:      {},
			TaskCancelOrder:    {},
		},
		after:   map[string]ports.ClaimCursor{},
		results: make(chan TaskResult, ExecutorSlots),
	}
}

// Results сообщает циклу о свободном месте после обработки задачи.
func (e *Executor) Results() <-chan TaskResult { return e.results }

// Wait вызывается после остановки Dispatch и отмены контекста исполнения.
func (e *Executor) Wait() { e.wg.Wait() }

// Dispatch обнаруживает новую работу всех полос на очередном проходе.
func (e *Executor) Dispatch(ctx context.Context) error {
	var failed error
	for _, lane := range executorLanes {
		failed = errors.Join(failed, e.dispatchLane(ctx, lane))
	}
	return failed
}

// Refill проверяет только полосу освободившегося места.
func (e *Executor) Refill(ctx context.Context, kind string) error {
	for _, lane := range executorLanes {
		if lane.kind == kind {
			return e.dispatchLane(ctx, lane)
		}
	}
	return nil
}

func (e *Executor) dispatchLane(ctx context.Context, lane executorLane) error {
	// Создание заказа ждёт покупатель (CreationTimeout): полоса берёт соединения своего
	// пула, их не займут индексация outbox и фоновые циклы.
	ctx = ports.WithDatabaseClass(ctx, ports.DatabaseOrders)
	e.mu.Lock()
	defer e.mu.Unlock()
	kind := lane.kind
	limit := lane.workers
	slots := make(map[ids.TenantID]int, len(e.active[kind]))
	for tenant, count := range e.active[kind] {
		limit -= count
		slots[tenant] = lane.perTenant - count
	}
	if limit == 0 {
		return nil
	}
	request := ports.ClaimRequest{
		Limit:       limit,
		PerTenant:   lane.perTenant,
		Kinds:       []string{kind},
		TenantSlots: slots,
		After:       e.after[kind],
	}
	if kind == TaskCreateOrder && !e.allowCreation(&request) {
		return nil
	}
	request.Now = e.clock.Now()
	request.LeaseUntil = request.Now.Add(taskLease)
	claimCtx, cancel := ports.WithTimeout(ctx, e.clock, taskStoreBudget)
	defer cancel()
	claimed, err := e.queue.Claim(claimCtx, request)
	if err != nil {
		return err
	}
	e.after[kind] = claimed.Next
	for _, task := range claimed.Tasks {
		e.active[kind][task.TenantID]++
		e.wg.Add(1)
		safego.Go("checkout.executor", func() { e.execute(ctx, task) })
	}
	return nil
}

func (e *Executor) allowCreation(request *ports.ClaimRequest) bool {
	if e.gate == nil {
		return true
	}
	state := e.gate.ServerState()
	if state == ports.PlatformServerBlocked {
		return false
	}
	probe := state == ports.PlatformServerProbe
	if probe {
		// Активная задача может ещё не дойти до HTTP и не занять пробу в ограждении.
		if request.Limit < CreationWorkers {
			return false
		}
		request.Limit = 1
	}
	preferRecent, excluded := e.gate.CreationCandidates(probe)
	request.PreferRecent = preferRecent
	if probe {
		for _, tenant := range excluded {
			request.TenantSlots[tenant] = 0
		}
	} else if preferRecent {
		// Пока ищем отвечающие магазины, второе место уже занятого магазина
		// используем только при отсутствии свежего кандидата.
		request.DeprioritizedTenants = excluded
		for tenant := range request.TenantSlots {
			request.DeprioritizedTenants = append(request.DeprioritizedTenants, tenant)
		}
	}
	return true
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
	err := errs.Guard(func() error { return e.handle(ctx, task) })
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

// handle ограничивает обработчик сроком, из которого считается аренда: без срока записи итога
// с повторами могли пережить аренду, и задачу взял бы второй исполнитель с повторным вызовом магазина.
func (e *Executor) handle(ctx context.Context, task ports.Task) error {
	handling, cancel := ports.WithTimeout(ctx, e.clock, 2*e.service.orderCallBudget()+taskStoreBudget)
	defer cancel()
	return e.service.HandleOrderTask(handling, task)
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
	// Отказ размыкателя: до его допуска повтор получит тот же отказ — откладываем, а не крутим.
	var rejected *ports.PlatformRejected
	if errors.As(err, &rejected) && resolution.NextAt != nil && resolution.NextAt.Before(rejected.NotBefore) {
		resolution.NextAt = &rejected.NotBefore
	}
	return resolution
}

// save не записывает отказ при SIGTERM: задача остаётся арендованной до повторного Claim.
func (e *Executor) save(ctx context.Context, resolution ports.TaskResolution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	saving, cancel := ports.WithTimeout(ctx, e.clock, taskStoreBudget)
	defer cancel()
	return e.queue.Resolve(saving, []ports.TaskResolution{resolution})
}
