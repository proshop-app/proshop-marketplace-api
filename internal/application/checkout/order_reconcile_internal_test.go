package checkout

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

var reconcileStart = time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)

// shopCall — запрос сверки магазину: кому и сколько заказов.
type shopCall struct {
	tenant ids.TenantID
	size   int
}

// stubStatuses — сервер магазинов: hung не отвечают в срок, slow — на пачке больше limit заказов,
// rejected — отклоняет размыкатель магазина до момента notBefore.
type stubStatuses struct {
	ports.PlatformAPI
	mu        sync.Mutex
	clock     *fake.Clock
	hung      map[ids.TenantID]bool
	limit     int
	rejected  ids.TenantID
	notBefore time.Time
	// healthyStarted и answerGate удерживают волну здоровых магазинов в полёте.
	healthyStarted chan struct{}
	answerGate     <-chan struct{}
	calls          []shopCall
}

func TestReconcileShopsKeepsShopTogether(t *testing.T) {
	var entries []checkout.ReconcileEntry
	for range 2*ReconcileBatch + 50 {
		entries = append(entries, checkout.ReconcileEntry{TenantID: "t1"})
	}
	for range 3 {
		entries = append(entries, checkout.ReconcileEntry{TenantID: "t2"})
	}
	shops := reconcileShops(entries)
	if len(shops) != 2 || len(shops[0]) != 2*ReconcileBatch+50 || len(shops[1]) != 3 {
		t.Fatalf("магазин — одна очередь заказов: %d", len(shops))
	}
	if len(reconcileShops(nil)) != 0 {
		t.Fatal("пустая страница — ни одного магазина")
	}
}

// Магазин отвечает в срок только на пачках до 50 заказов: повторные круги уменьшают пачку и сверяют
// всё, без пауз в строю.
func TestReconcileShopShrinksBatchUntilShopAnswers(t *testing.T) {
	service, platform := reconcileService(&stubStatuses{limit: 50})
	run := service.reconcileAll(t, reconcileEntries(1, ReconcileBatch))
	if len(run.result.Failed) != 0 || run.result.Checked != ReconcileBatch {
		t.Fatalf("все заказы магазина сверены: %+v", run.result)
	}
	if got := platform.sizes(); !slices.Equal(got, []int{200, 100, 50, 50, 50, 50}) {
		t.Fatalf("размеры запросов: %v", got)
	}
	if platform.paused() != 0 {
		t.Fatalf("проход не спит в строю: %s", platform.paused())
	}
}

// 400 магазинов, каждый десятый завис: здоровые получают ответ первым кругом, ни один не ждёт
// повторов зависших; зависшие повторяются кругами после и пропускаются; пауз нет.
func TestReconcileHealthyShopsDoNotWaitForHungOnes(t *testing.T) {
	hung := map[ids.TenantID]bool{}
	for i := 0; i < 400; i += 10 {
		hung[shopName(i)] = true
	}
	service, platform := reconcileService(&stubStatuses{hung: hung})
	run := service.reconcileAll(t, reconcileEntries(400, 1))
	if len(run.result.Failed) != len(hung) || run.result.Checked != 400-len(hung) || run.stopped() != nil {
		t.Fatalf("здоровые сверены, зависшие пропущены: %+v %v", run.result, run.stopped())
	}
	calls := platform.log()
	lastHealthy, firstRetry := 0, len(calls)
	seen := map[ids.TenantID]int{}
	for i, call := range calls {
		seen[call.tenant]++
		if !hung[call.tenant] {
			lastHealthy = i
		}
		if hung[call.tenant] && seen[call.tenant] == 2 && i < firstRetry {
			firstRetry = i
		}
	}
	if lastHealthy > firstRetry {
		t.Fatalf("здоровый магазин ждал повтора зависшего: %d после %d", lastHealthy, firstRetry)
	}
	for tenant, count := range seen {
		want := 1
		if hung[tenant] {
			want = 1 + ReconcileRetries
		}
		if count != want {
			t.Fatalf("магазину %s запросов %d, ожидалось %d", tenant, count, want)
		}
	}
	if len(calls) != 400+len(hung)*ReconcileRetries || platform.paused() != 0 {
		t.Fatalf("запросов %d, пауз %s", len(calls), platform.paused())
	}
}

// Сервер принимает соединения и молчит: набралось молчание без единого ответа — проход дожидается
// начатых вызовов и прерывается, не спрашивая остальные магазины лежащий сервер; пауз нет.
func TestReconcileSilentServerStopsPass(t *testing.T) {
	for _, shops := range []int{80, 1000} {
		hung := map[ids.TenantID]bool{}
		for i := range shops {
			hung[shopName(i)] = true
		}
		service, platform := reconcileService(&stubStatuses{hung: hung})
		run := newReconcileRun()
		service.reconcilePage(context.Background(), reconcileEntries(shops, 1), run)
		_, err := service.finishReconcile(context.Background(), run, platform.clock.Now())
		calls := len(platform.log())
		if !errors.Is(err, ErrReconcileServerDown) || calls > 4*reconcileWorkers || platform.paused() != 0 {
			t.Fatalf("%d магазинов: %v, запросов %d, пауз %s", shops, err, calls, platform.paused())
		}
	}
}

// Скопление зависших магазинов подряд при живом сервере не прерывает проход: все здоровые сверены.
func TestReconcileRunOfHungShopsDoesNotStopPass(t *testing.T) {
	for _, run := range []struct {
		name  string
		first int
		count int
	}{
		{name: "20 подряд среди 400", first: 150, count: 20},
		{name: "24 в начале", first: 0, count: 24},
	} {
		hung := map[ids.TenantID]bool{}
		for i := run.first; i < run.first+run.count; i++ {
			hung[shopName(i)] = true
		}
		started := make(chan struct{}, 400)
		answer := make(chan struct{})
		service, platform := reconcileService(&stubStatuses{hung: hung, healthyStarted: started, answerGate: answer})
		ctx := context.Background()
		result := newReconcileRun()
		done := make(chan struct{})
		go func() {
			defer close(done)
			service.reconcilePage(ctx, reconcileEntries(400, 1), result)
		}()
		for range reconcileWorkers {
			<-started
		}
		// В начальной серии все таймауты уже учтены, а здоровые магазины ещё не ответили.
		silentDuringWave := result.serverSilent()
		close(answer)
		<-done
		if run.first == 0 && !silentDuringWave {
			t.Fatal("ожидалась полная выборка таймаутов перед ответом здорового магазина")
		}
		if _, err := service.finishReconcile(ctx, result, platform.clock.Now()); err != nil {
			t.Fatal(err)
		}
		if result.stopped() != nil || result.result.Checked != 400-run.count || len(result.result.Failed) != run.count {
			t.Fatalf("%s: %+v %v", run.name, result.result, result.stopped())
		}
	}
}

func TestReconcileSamplesAnotherWaveAfterFastTimeouts(t *testing.T) {
	service, _ := reconcileService(&stubStatuses{})
	run := newReconcileRun()
	run.scheduled = reconcileSilentShops
	run.silent = reconcileSilentShops
	var group errgroup.Group
	if service.serverDown(&group, run) {
		t.Fatal("быстрые отказы сократили выборку до следующей волны магазинов")
	}
	run.scheduled += reconcileWorkers
	if !service.serverDown(&group, run) || !errors.Is(run.stopped(), ErrPlatformSilent) {
		t.Fatal("после дополнительной волны без ответов проход должен остановиться")
	}
}

// Размыкатель магазина отклоняет до своего срока: проход один раз ждёт его после остальных и
// повторяет запрос; пауза — одна на проход, а не на магазин.
func TestReconcileRetriesShopAfterItsBreaker(t *testing.T) {
	clock := fake.NewClock(reconcileStart)
	service, platform := reconcileService(&stubStatuses{
		rejected:  shopName(3),
		notBefore: clock.Now().Add(5 * time.Second),
		clock:     clock,
	})
	run := service.reconcileAll(t, reconcileEntries(10, 1))
	if len(run.result.Failed) != 0 || run.result.Checked != 10 {
		t.Fatalf("магазин сверен после своего размыкателя: %+v", run.result)
	}
	if platform.paused() != 5*time.Second || len(platform.log()) != 11 {
		t.Fatalf("одна пауза до срока размыкателя: %s, запросов %d", platform.paused(), len(platform.log()))
	}
}

// Размыкатель магазина откроется позже бюджета повторов: проход его не ждёт, магазин пропускается.
func TestReconcileDoesNotWaitBreakerBeyondBudget(t *testing.T) {
	clock := fake.NewClock(reconcileStart)
	service, platform := reconcileService(&stubStatuses{
		rejected:  shopName(3),
		notBefore: clock.Now().Add(reconcileRetryBudget + time.Minute),
		clock:     clock,
	})
	run := service.reconcileAll(t, reconcileEntries(10, 1))
	if len(run.result.Failed) != 1 || platform.paused() != 0 || len(platform.log()) != 10 {
		t.Fatalf("ожидание за пределом бюджета: %+v, пауз %s", run.result, platform.paused())
	}
}

// Бюджет повторных кругов исчерпан с начала прохода: зависший магазин не повторяется, а пропускается.
func TestReconcileRetryRoundsStopAtBudget(t *testing.T) {
	service, platform := reconcileService(&stubStatuses{hung: map[ids.TenantID]bool{shopName(3): true}})
	run := newReconcileRun()
	service.reconcilePage(context.Background(), reconcileEntries(10, 1), run)
	started := platform.clock.Now().Add(-reconcileRetryBudget - time.Minute)
	if _, err := service.finishReconcile(context.Background(), run, started); err != nil {
		t.Fatal(err)
	}
	if len(platform.log()) != 10 || run.result.Failed[shopName(3)] == nil {
		t.Fatalf("за пределом бюджета повторов нет: запросов %d, итог %+v", len(platform.log()), run.result)
	}
}

func reconcileService(platform *stubStatuses) (*Service, *stubStatuses) {
	if platform.clock == nil {
		platform.clock = fake.NewClock(reconcileStart)
	}
	return NewService(nil, platform, nil, nil, nil, platform.clock, Config{}), platform
}

func (s *Service) reconcileAll(t *testing.T, entries []checkout.ReconcileEntry) *reconcileRun {
	t.Helper()
	run := newReconcileRun()
	s.reconcilePage(context.Background(), entries, run)
	if _, err := s.finishReconcile(context.Background(), run, s.clock.Now()); err != nil {
		t.Fatal(err)
	}
	return run
}

// reconcileEntries — страница сверки: shops магазинов по orders совпадающих заказов, по порядку магазина.
func reconcileEntries(shops, orders int) []checkout.ReconcileEntry {
	entries := make([]checkout.ReconcileEntry, 0, shops*orders)
	for i := range shops {
		for j := range orders {
			entries = append(entries, checkout.ReconcileEntry{
				TenantID: shopName(i),
				OrderID:  fmt.Sprintf("%s-o%d", shopName(i), j),
				Status:   checkout.GroupPaid,
				Revision: 1,
			})
		}
	}
	return entries
}

func shopName(i int) ids.TenantID { return ids.TenantID(fmt.Sprintf("t%04d", i)) }

func (p *stubStatuses) OrderStatuses(
	_ context.Context,
	tenant ids.TenantID,
	orderIDs []string,
) ([]checkout.OrderStatus, error) {
	p.mu.Lock()
	p.calls = append(p.calls, shopCall{tenant: tenant, size: len(orderIDs)})
	p.mu.Unlock()
	switch {
	case p.hung[tenant], p.limit > 0 && len(orderIDs) > p.limit:
		return nil, ports.ErrPlatformTimeout.Wrap(context.DeadlineExceeded)
	case tenant == p.rejected && p.clock.Now().Before(p.notBefore):
		return nil, &ports.PlatformRejected{NotBefore: p.notBefore, Cause: errors.New("размыкатель магазина")}
	}
	if p.answerGate != nil {
		p.healthyStarted <- struct{}{}
		<-p.answerGate
	}
	statuses := make([]checkout.OrderStatus, 0, len(orderIDs))
	for _, id := range orderIDs {
		statuses = append(statuses, checkout.OrderStatus{OrderID: id, Status: checkout.GroupPaid, Revision: 1})
	}
	return statuses, nil
}

func (p *stubStatuses) paused() time.Duration {
	return p.clock.Now().Sub(reconcileStart)
}

func (p *stubStatuses) log() []shopCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

func (p *stubStatuses) sizes() []int {
	sizes := []int{}
	for _, call := range p.log() {
		sizes = append(sizes, call.size)
	}
	return sizes
}
