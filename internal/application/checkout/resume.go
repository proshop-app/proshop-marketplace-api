package checkout

import (
	"context"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// resumedPage — итог страницы восстановления: сколько покупок взято, курсор и сохранённые состояния.
type resumedPage struct {
	taken  int
	last   ids.UUID
	states []checkout.State
}

// Resume работает и при запуске, и по расписанию. Страница созревших покупок разбирается одной
// транзакцией под блокировкой строк: покупку, занятую событием, покупателем или соседним проходом,
// страница пропускает. Возврат в корзину идёт после, по группе: он пишет корзину покупателя,
// и сбой одной корзины не откатывает работу остальных покупок страницы.
func (s *Service) Resume(ctx context.Context) error {
	var failures []error
	var after ids.UUID
	for {
		page, err := s.resumePage(ctx, after)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		for _, state := range page.states {
			if err := s.returnDue(ctx, state); err != nil {
				failures = append(failures, err)
			}
		}
		if page.taken < orderBatch {
			return errors.Join(failures...)
		}
		after = page.last
	}
}

// resumePage ставит задачи опроса и отмены страницы одной пачкой и сохраняет изменившиеся покупки
// одним запросом.
func (s *Service) resumePage(ctx context.Context, after ids.UUID) (resumedPage, error) {
	var page resumedPage
	var purchases []ports.NewTask
	now := s.clock.Now()
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		page, purchases = resumedPage{}, nil
		found, err := tx.Checkouts().LockWorkDue(ctx, now, after, orderBatch)
		if err != nil || len(found) == 0 {
			return err
		}
		var changed []*checkout.Checkout
		var tasks []ports.NewTask
		for _, c := range found {
			previous := c.State()
			due, err := s.resumeWork(c, now)
			if err != nil {
				return err
			}
			tasks = append(tasks, due...)
			state := c.State()
			if state.Version != previous.Version {
				changed = append(changed, c)
			}
			s.observeTransitions(tx, previous, state)
			s.collectSearchPurchases(ctx, tx, previous, state, &purchases)
			page.states = append(page.states, state)
		}
		page.taken, page.last = len(found), found[len(found)-1].ID()
		if err := tx.Checkouts().UpdateMany(ctx, changed); err != nil || len(tasks) == 0 {
			return err
		}
		return tx.Outbox().EnqueueBatch(ctx, tasks)
	})
	if err == nil {
		s.enqueueSearchPurchases(ctx, purchases)
	}
	return page, err
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
