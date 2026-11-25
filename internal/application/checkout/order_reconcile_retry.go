package checkout

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/safego"
)

// ErrReconcileServerDown — сверка прервана: сервер магазинов недоступен целиком. Итог прохода до
// прерывания возвращается вместе с ошибкой.
var ErrReconcileServerDown = errs.New(
	errs.Unavailable,
	"checkout.reconcile_server_down",
	"сервер магазинов недоступен, сверка прервана",
)

// pendingShop — заказы магазина, ждущие сверки, и размер его пачки.
type pendingShop struct {
	tenant  ids.TenantID
	entries []checkout.ReconcileEntry
	size    int
	// timeouts — истёкшие сроки чтения; breaker — магазин уже ждал своего размыкателя.
	timeouts int
	breaker  bool
	// notBefore — раньше размыкатель магазина отклонит запрос так же.
	notBefore time.Time
	cause     error
}

// reconcileRun — общее состояние прохода для параллельных магазинов.
type reconcileRun struct {
	mu        sync.Mutex
	result    ReconcileResult
	postponed map[ids.TenantID]*pendingShop
	// silent — истёкшие сроки подряд без единого ответа в первом круге.
	silent int
	// serverDown — сервер магазинов недоступен: остальные магазины получат тот же отказ.
	serverDown error
}

func newReconcileRun() *reconcileRun {
	return &reconcileRun{
		result:    ReconcileResult{Failed: map[ids.TenantID]error{}},
		postponed: map[ids.TenantID]*pendingShop{},
	}
}

// finishReconcile проходит повторные круги и возвращает итог; при недоступном сервере — итог до
// прерывания вместе с ErrReconcileServerDown, чтобы уже найденные расхождения были учтены.
func (s *Service) finishReconcile(ctx context.Context, run *reconcileRun, started time.Time) (ReconcileResult, error) {
	if run.stopped() == nil && ctx.Err() == nil {
		s.retryPostponed(ctx, run, started)
	}
	run.skipPostponed()
	if cause := run.stopped(); cause != nil {
		return run.result, ErrReconcileServerDown.Wrap(cause)
	}
	return run.result, ctx.Err()
}

// retryPostponed — повторные круги по отложенным магазинам, после всех остальных. Пауз в строю нет:
// время до повтора — это сверка остальных магазинов; ждать проход может один раз — момента, когда
// размыкатель магазина снова пропустит запрос, если он в пределах бюджета.
func (s *Service) retryPostponed(ctx context.Context, run *reconcileRun, started time.Time) {
	waited := false
	for round := 0; round <= ReconcileRetries+1 && run.stopped() == nil; round++ {
		now := s.clock.Now()
		if now.Sub(started) > reconcileRetryBudget {
			return
		}
		ready, until := run.takeReady(now)
		if len(ready) == 0 {
			if until.IsZero() || waited || until.Sub(started) > reconcileRetryBudget {
				return
			}
			waited = true
			if !s.clock.Wait(ctx, until.Sub(now)) {
				return
			}
			continue
		}
		s.reconcileReady(ctx, ready, run)
	}
}

// reconcileReady сверяет готовые к повтору магазины параллельно.
func (s *Service) reconcileReady(ctx context.Context, shops []pendingShop, run *reconcileRun) {
	var group errgroup.Group
	group.SetLimit(reconcileWorkers)
	for _, shop := range shops {
		group.Go(safego.Guard("checkout.reconcile-retry", func() error {
			s.reconcileShop(ctx, shop, run, false)
			return nil
		}))
	}
	_ = group.Wait()
}

// postpone решает судьбу магазина после отказа. Размыкатель сервера — прервать проход. Истёк срок
// чтения — повторный круг вдвое меньшей пачкой; серия таких отказов подряд в первом круге —
// признак сервера. Размыкатель магазина — один повтор после его срока. Остальное — пропуск.
func (r *reconcileRun) postpone(shop pendingShop, err error, first bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	shop.cause = err
	var rejected *ports.PlatformRejected
	switch {
	case errors.As(err, &rejected) && rejected.Server:
		r.serverDownLocked(err)
	case errors.As(err, &rejected) && !shop.breaker:
		shop.breaker, shop.notBefore = true, rejected.NotBefore
		r.postponed[shop.tenant] = &shop
	case errors.Is(err, ports.ErrPlatformTimeout):
		r.timeoutLocked(shop, first)
	default:
		r.skipLocked(shop)
	}
}

func (r *reconcileRun) timeoutLocked(shop pendingShop, first bool) {
	if first {
		r.silent++
		if r.silent >= reconcileServerTimeouts {
			r.serverDownLocked(shop.cause)
			r.postponed[shop.tenant] = &shop
			return
		}
	}
	shop.timeouts++
	shop.size = max(1, shop.size/2)
	if shop.timeouts > ReconcileRetries {
		r.skipLocked(shop)
		return
	}
	r.postponed[shop.tenant] = &shop
}

// answered — магазин ответил: серия молчания прервана.
func (r *reconcileRun) answered() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.silent = 0
}

// joinPostponed добавляет заказы следующей страницы к отложенному магазину; false — не отложен.
func (r *reconcileRun) joinPostponed(shop pendingShop) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending, found := r.postponed[shop.tenant]
	if found {
		pending.entries = append(pending.entries, shop.entries...)
	}
	return found
}

// takeReady забирает отложенные магазины, которым можно повторить запрос; until — ближайший момент
// для ждущих своего размыкателя.
func (r *reconcileRun) takeReady(now time.Time) ([]pendingShop, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ready []pendingShop
	var until time.Time
	for tenant, shop := range r.postponed {
		if now.Before(shop.notBefore) {
			if until.IsZero() || shop.notBefore.Before(until) {
				until = shop.notBefore
			}
			continue
		}
		ready = append(ready, *shop)
		delete(r.postponed, tenant)
	}
	return ready, until
}

// skipPostponed — магазины, не сверенные к концу прохода, отмечаются с записью в журнал.
func (r *reconcileRun) skipPostponed() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for tenant, shop := range r.postponed {
		r.skipLocked(*shop)
		delete(r.postponed, tenant)
	}
}

// skipLocked отмечает магазин несверенным в этом проходе: его заказы сверит следующий.
func (r *reconcileRun) skipLocked(shop pendingShop) {
	r.result.Failed[shop.tenant] = shop.cause
	slog.Warn(
		"сверка заказов: магазин не ответил, заказы сверит следующий проход",
		slog.String("tenantId", string(shop.tenant)),
		slog.Int("unchecked", len(shop.entries)),
		slog.Int("timeouts", shop.timeouts),
		slog.Any("error", shop.cause),
	)
}

func (r *reconcileRun) serverDownLocked(cause error) {
	if r.serverDown == nil {
		r.serverDown = cause
	}
}

func (r *reconcileRun) stopped() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.serverDown
}

func (r *reconcileRun) failed(tenant ids.TenantID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result.Failed[tenant] != nil
}

func (r *reconcileRun) count(outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result.Checked++
	switch outcome {
	case eventStatusMismatch:
		r.result.Mismatched++
	case eventStatusMissing:
		r.result.Missing++
	case eventStatusUnresolved:
		r.result.Unresolved++
	}
}
