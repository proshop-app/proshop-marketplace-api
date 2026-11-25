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

// resumeBrokenDelay — на сколько откладывается работа покупки, которую проход не смог разобрать:
// она не занимает каждый проход и не теряется, ошибка повторяется в журнале раз в этот срок.
const resumeBrokenDelay = 10 * time.Minute

// resumedPage — итог страницы восстановления.
type resumedPage struct {
	// taken — покупки, взятые под блокировку: если страница откатилась, они разбираются по одной.
	taken    []ids.UUID
	states   []checkout.State
	failures []error
	next     checkout.WorkCursor
	read     bool
	more     bool
}

// resumeBatch — работа страницы, собранная до записи.
type resumeBatch struct {
	changed  []*checkout.Checkout
	tasks    []ports.NewTask
	broken   []ids.UUID
	failures []error
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
		failures = append(failures, page.failures...)
		if err != nil {
			states, separate := s.resumeEach(ctx, page.taken)
			page.states = states
			failures = append(failures, err)
			failures = append(failures, separate...)
		}
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
		page.failures = batch.failures
		return s.saveResume(ctx, tx, batch, now)
	})
	if err != nil {
		page.states, page.failures = nil, nil
		return page, err
	}
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
	for _, broken := range work.Broken {
		batch.reject(broken.ID, "", broken.Err)
	}
	for _, c := range work.Due {
		page.taken = append(page.taken, c.ID())
		previous := c.State()
		tasks, err := s.resumeWork(c, now)
		if err != nil {
			batch.reject(c.ID(), groupTenants(previous), err)
			continue
		}
		state := c.State()
		batch.tasks = append(batch.tasks, tasks...)
		if state.Version != previous.Version {
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
	if err := tx.Checkouts().DeferWork(ctx, batch.broken, now.Add(resumeBrokenDelay)); err != nil {
		return err
	}
	if len(batch.tasks) == 0 {
		return nil
	}
	return tx.Outbox().EnqueueBatch(ctx, batch.tasks)
}

// resumeEach разбирает покупки откатившейся страницы по одной: сбой одной не задерживает остальные.
func (s *Service) resumeEach(ctx context.Context, taken []ids.UUID) ([]checkout.State, []error) {
	var states []checkout.State
	var failures []error
	for _, id := range taken {
		state, err := s.resumeOne(ctx, id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		states = append(states, state)
	}
	return states, failures
}

// resumeOne — работа одной покупки своей транзакцией; неразобравшаяся откладывается.
func (s *Service) resumeOne(ctx context.Context, id ids.UUID) (checkout.State, error) {
	var state checkout.State
	var rejected error
	now := s.clock.Now()
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		rejected = nil
		c, err := tx.Checkouts().GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		previous := c.State()
		tasks, err := s.resumeWork(c, now)
		if err != nil {
			var batch resumeBatch
			batch.reject(id, groupTenants(previous), err)
			rejected = batch.failures[0]
			return tx.Checkouts().DeferWork(ctx, batch.broken, now.Add(resumeBrokenDelay))
		}
		state = c.State()
		batch := resumeBatch{tasks: tasks}
		if state.Version != previous.Version {
			batch.changed = []*checkout.Checkout{c}
		}
		return s.saveResume(ctx, tx, batch, now)
	})
	if err == nil {
		err = rejected
	}
	return state, err
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

// reject откладывает покупку и пишет причину в журнал: магазины группы и покупка.
func (b *resumeBatch) reject(id ids.UUID, tenants string, cause error) {
	b.broken = append(b.broken, id)
	b.failures = append(b.failures, cause)
	slog.Warn(
		"восстановление покупок: покупка не разобрана, работа отложена",
		slog.String("checkoutId", string(id)),
		slog.String("tenantId", tenants),
		slog.Duration("delay", resumeBrokenDelay),
		slog.Any("error", cause),
	)
}

func groupTenants(state checkout.State) string {
	tenants := make([]string, 0, len(state.Groups))
	for _, g := range state.Groups {
		tenants = append(tenants, string(g.TenantID))
	}
	return strings.Join(tenants, ",")
}
