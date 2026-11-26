package checkout

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// resumeBrokenDelay — на сколько откладывается работа покупки, которую проход не смог разобрать:
	// она не занимает каждый проход и не теряется, ошибка повторяется в журнале раз в этот срок.
	resumeBrokenDelay = 10 * time.Minute
	// eventResumeDeferred — событие оформления: работа покупки отложена; покупку находит оператор
	// фильтром «зависшие».
	eventResumeDeferred = "resume_deferred"
)

// resumedPage — итог страницы восстановления.
type resumedPage struct {
	// taken — покупки, взятые под блокировку, и broken — неразобравшиеся: если страница откатилась,
	// первые разбираются по одной, вторые откладываются заново.
	taken    []ids.UUID
	broken   []rejectedPurchase
	states   []checkout.State
	failures []error
	next     checkout.WorkCursor
	read     bool
	more     bool
}

// rejectedPurchase — покупка, которую проход откладывает, с магазинами её групп и причиной.
type rejectedPurchase struct {
	id      ids.UUID
	tenants []ids.TenantID
	cause   error
}

// resumeBatch — работа страницы, собранная до записи.
type resumeBatch struct {
	changed []*checkout.Checkout
	tasks   []ports.NewTask
	broken  []rejectedPurchase
}

// Resume работает и при запуске, и по расписанию. Страница созревших покупок разбирается одной
// транзакцией под блокировкой строк: покупку, занятую событием, покупателем или соседним проходом,
// страница пропускает. Покупка, которую не удалось разобрать, откладывается, остальные сохраняются;
// откатившаяся страница разбирается по одной покупке. Возврат в корзину идёт после, по группе.
func (s *Service) Resume(ctx context.Context) error {
	var failures []error
	var cursor checkout.WorkCursor
	for {
		page, err := s.resumePage(ctx, cursor)
		if err != nil && !page.read {
			return errors.Join(append(failures, err)...)
		}
		if err != nil {
			page.states, page.failures = s.resumeEach(ctx, page)
			failures = append(failures, err)
		}
		failures = append(failures, page.failures...)
		for _, state := range page.states {
			if err := s.returnDue(ctx, state); err != nil {
				failures = append(failures, err)
			}
		}
		if !page.more {
			return errors.Join(failures...)
		}
		cursor = page.next
	}
}

// resumePage берёт страницу и сохраняет её работу одной транзакцией.
func (s *Service) resumePage(ctx context.Context, cursor checkout.WorkCursor) (resumedPage, error) {
	var page resumedPage
	var purchases []ports.NewTask
	now := s.clock.Now()
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		page, purchases = resumedPage{}, nil
		work, err := tx.Checkouts().LockWorkDue(ctx, now, cursor, orderBatch)
		if err != nil {
			return err
		}
		page.read, page.next, page.more = true, work.Next, work.More
		batch := s.collectResume(ctx, tx, work, now, &page, &purchases)
		page.broken = batch.broken
		return s.saveResume(ctx, tx, batch, now)
	})
	if err != nil {
		page.states = nil
		return page, err
	}
	page.failures = rejectionCauses(page.broken)
	s.enqueueSearchPurchases(ctx, purchases)
	return page, nil
}

// collectResume собирает задачи и изменения покупок страницы; неразобравшиеся откладываются.
func (s *Service) collectResume(
	ctx context.Context,
	tx ports.Tx,
	work checkout.WorkPage,
	now time.Time,
	page *resumedPage,
	purchases *[]ports.NewTask,
) resumeBatch {
	var batch resumeBatch
	deferred := map[ids.UUID]bool{}
	for _, id := range work.Deferred {
		deferred[id] = true
	}
	for _, broken := range work.Broken {
		batch.broken = append(batch.broken, rejectedPurchase{id: broken.ID, cause: broken.Err})
	}
	for _, c := range work.Due {
		previous := c.State()
		tasks, err := s.resumeWork(c, now)
		if err != nil {
			rejected := rejectedPurchase{id: c.ID(), tenants: groupTenants(previous), cause: err}
			batch.broken = append(batch.broken, rejected)
			continue
		}
		page.taken = append(page.taken, c.ID())
		state := c.State()
		batch.tasks = append(batch.tasks, tasks...)
		if state.Version != previous.Version || deferred[c.ID()] {
			batch.changed = append(batch.changed, c)
		}
		s.observeTransitions(tx, previous, state)
		s.collectSearchPurchases(ctx, tx, previous, state, purchases)
		page.states = append(page.states, state)
	}
	return batch
}

// saveResume — изменившиеся покупки одним запросом, отложенные — одним, задачи — одной пачкой.
func (s *Service) saveResume(ctx context.Context, tx ports.Tx, batch resumeBatch, now time.Time) error {
	if err := tx.Checkouts().UpdateMany(ctx, batch.changed); err != nil {
		return err
	}
	if err := s.deferPurchases(ctx, tx, batch.broken, now); err != nil {
		return err
	}
	if len(batch.tasks) == 0 {
		return nil
	}
	return tx.Outbox().EnqueueBatch(ctx, batch.tasks)
}

// resumeEach разбирает покупки откатившейся страницы по одной: сбой одной не задерживает остальные.
// Неразобравшиеся строки страницы откладываются заново — их отметка откатилась вместе со страницей.
func (s *Service) resumeEach(ctx context.Context, page resumedPage) ([]checkout.State, []error) {
	var states []checkout.State
	failures := rejectionCauses(page.broken)
	if err := s.deferSeparately(ctx, page.broken); err != nil {
		failures = append(failures, err)
	}
	for _, id := range page.taken {
		state, err := s.resumeOne(ctx, id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		states = append(states, state)
	}
	return states, failures
}

// resumeOne — работа одной покупки своей транзакцией. Покупка, которая не разобралась или не
// сохранилась, откладывается отдельной транзакцией: иначе она возвращалась бы первой в каждый проход.
func (s *Service) resumeOne(ctx context.Context, id ids.UUID) (checkout.State, error) {
	var state checkout.State
	var tenants []ids.TenantID
	now := s.clock.Now()
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.Checkouts().GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		previous := c.State()
		tenants = groupTenants(previous)
		tasks, err := s.resumeWork(c, now)
		if err != nil {
			return err
		}
		state = c.State()
		// Сохраняется и без изменений: покупка этого пути могла быть отложена, сохранение снимает признак.
		batch := resumeBatch{tasks: tasks, changed: []*checkout.Checkout{c}}
		return s.saveResume(ctx, tx, batch, now)
	})
	if err == nil || errors.Is(err, checkout.ErrNotFound) {
		return state, err
	}
	broken := []rejectedPurchase{{id: id, tenants: tenants, cause: err}}
	return checkout.State{}, errors.Join(err, s.deferSeparately(ctx, broken))
}

// deferSeparately откладывает покупки своей транзакцией.
func (s *Service) deferSeparately(ctx context.Context, broken []rejectedPurchase) error {
	if len(broken) == 0 {
		return nil
	}
	now := s.clock.Now()
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return s.deferPurchases(ctx, tx, broken, now)
	})
}

// deferPurchases откладывает работу покупок одним запросом; после фиксации — журнал с покупкой и
// магазинами и счётчик отложенных.
func (s *Service) deferPurchases(ctx context.Context, tx ports.Tx, broken []rejectedPurchase, now time.Time) error {
	if len(broken) == 0 {
		return nil
	}
	list := make([]ids.UUID, 0, len(broken))
	for _, purchase := range broken {
		list = append(list, purchase.id)
		tx.AfterCommit(func() { s.reportDeferred(purchase) })
	}
	return tx.Checkouts().DeferWork(ctx, list, now.Add(resumeBrokenDelay), now)
}

func (s *Service) reportDeferred(purchase rejectedPurchase) {
	tenants := make([]string, 0, len(purchase.tenants))
	for _, tenant := range purchase.tenants {
		tenants = append(tenants, string(tenant))
	}
	slog.Warn(
		"восстановление покупок: покупка не разобрана, работа отложена",
		slog.String("checkoutId", string(purchase.id)),
		slog.String("tenantId", strings.Join(tenants, ",")),
		slog.Duration("delay", resumeBrokenDelay),
		slog.Any("error", purchase.cause),
	)
	if s.observer == nil {
		return
	}
	if len(purchase.tenants) == 0 {
		s.observer.RecordCheckoutEvent("", purchase.id, eventResumeDeferred)
		return
	}
	s.observer.RecordCheckoutEvent(purchase.tenants[0], purchase.id, eventResumeDeferred)
}

// resumeWork — задачи созревшей работы покупки. Если созревшей работы у групп нет, срок устарел
// (его записала прежняя версия) — сохранение пересчитает его, иначе покупка выбиралась бы каждый круг.
func (s *Service) resumeWork(c *checkout.Checkout, now time.Time) ([]ports.NewTask, error) {
	if err := c.SettleWork(now); err != nil {
		return nil, err
	}
	var tasks []ports.NewTask
	for _, g := range c.Groups() {
		if !g.PollDue(now) {
			continue
		}
		task, err := newOrderTask(TaskPollOrder, c.State(), g, now)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	cancellations, err := s.queueCancellations(c)
	return append(tasks, cancellations...), err
}

func rejectionCauses(broken []rejectedPurchase) []error {
	causes := make([]error, 0, len(broken))
	for _, purchase := range broken {
		causes = append(causes, purchase.cause)
	}
	return causes
}

func groupTenants(state checkout.State) []ids.TenantID {
	tenants := make([]ids.TenantID, 0, len(state.Groups))
	for _, g := range state.Groups {
		tenants = append(tenants, g.TenantID)
	}
	return tenants
}
