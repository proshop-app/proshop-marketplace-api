package checkout_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type claimedCreationGate struct {
	ports.PlatformAPI
	started chan struct{}
	release chan struct{}
}

type differentOrderPickup struct {
	ports.PlatformAPI
}

type quoteBeforeCreationHook struct {
	ports.PlatformAPI
	once   sync.Once
	during func()
}

func TestRemovedUnreachableGroupAllowsHealthyOrders(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	created, err := e.service.Create(ctx, buyerID, key)
	if err != nil {
		t.Fatal(err)
	}
	e.platform.Fail("t1", errors.New("timeout"))
	quoted, quoteErr := e.service.Quote(ctx, buyerID, created.ID, "cdek", "MSK1", "Город")
	logPaymentState(t, "выбор пункта при недоступном t1", quoted, quoteErr)
	removed, removeErr := e.service.RemoveGroup(ctx, buyerID, created.ID, "t1")
	logPaymentState(t, "RemoveGroup t1", removed, removeErr)
	if removeErr != nil || removed.Groups[0].Quote != nil {
		t.Fatal("исключённая группа должна сбросить расчёт", removed, removeErr)
	}
	confirmed, confirmErr := e.service.Confirm(ctx, buyerID, created.ID)
	logPaymentState(t, "Confirm", confirmed, confirmErr)
	ordered, orderErr := beginAndExecute(ctx, e, buyerID, created.ID, orderContact)
	logPaymentState(t, "CreateOrders", ordered, orderErr)
	payErr := e.service.ConfirmPayment(ctx, buyerID, created.ID)
	t.Logf(
		"ConfirmPayment=%v; заказов t2=%d; оплаты=%v",
		payErr,
		len(e.platform.OrderRequests("t2")),
		e.platform.StubPaymentCalls(),
	)
	if confirmErr != nil || orderErr != nil || payErr != nil || len(e.platform.OrderRequests("t2")) != 1 {
		t.Fatalf("убранный недоступный магазин блокирует оформление остальных")
	}
	if ordered.Status == checkout.StatusDraft {
		t.Fatalf("покупка осталась черновиком")
	}
}

func addAlternativePickup(t *testing.T, e *env, code, region string) {
	t.Helper()
	for _, tenant := range []ids.TenantID{"t1", "t2", "t3"} {
		points := []delivery.PickupPoint{}
		for _, c := range []struct{ code, region string }{{"MSK1", "RU-MOW"}, {code, region}} {
			point, err := delivery.NewPickupPoint(
				"cdek",
				c.code,
				"Пункт "+c.code,
				"ул. Примерная, 1",
				"Город",
				delivery.Location{Lat: 55.7, Lon: 37.6},
				"",
			)
			if err != nil {
				t.Fatal(err)
			}
			point.RegionCode = c.region
			points = append(points, point)
		}
		e.platform.SetPickupPoints(tenant, ports.PickupPoints{Available: true, Items: points})
	}
}

func TestConcurrentPickupChangesPreserveOrderDestination(t *testing.T) {
	violations := 0
	outcomes := map[string]int{}
	for seed := int64(0); seed < 300; seed++ {
		e := setup(t)
		ctx := context.Background()
		addConsentThirdShop(t, e)
		addAlternativePickup(t, e, "MSK2", "RU-MOW")
		created, err := e.service.Create(ctx, buyerID, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.service.Quote(ctx, buyerID, created.ID, "cdek", "MSK1", "Город"); err != nil {
			t.Fatal(err)
		}
		rng := rand.New(rand.NewSource(seed))
		var logMu sync.Mutex
		var events []string
		note := func(format string, args ...any) {
			logMu.Lock()
			events = append(events, fmt.Sprintf(format, args...))
			logMu.Unlock()
		}
		ops := []func(){
			func() {
				_, err := e.service.Quote(ctx, buyerID, created.ID, "cdek", "MSK2", "Город")
				note("Quote MSK2: %v", err)
			},
			func() {
				_, err := e.service.Quote(ctx, buyerID, created.ID, "cdek", "MSK1", "Город")
				note("Quote MSK1: %v", err)
			},
			func() {
				st, err := e.service.Confirm(ctx, buyerID, created.ID)
				note("A Confirm: %v %v", err, st.Delivery)
				st, err = e.service.CreateOrders(ctx, buyerID, created.ID, orderContact)
				note("A CreateOrders: %v %s %v", err, st.Status, st.Delivery)
				note("A tasks: %v", executeTasks(ctx, e))
				note("A pay: %v", e.service.ConfirmPayment(ctx, buyerID, created.ID))
			},
			func() {
				st, err := e.service.Confirm(ctx, buyerID, created.ID)
				note("B Confirm: %v %v", err, st.Delivery)
				st, err = e.service.CreateOrders(ctx, buyerID, created.ID, orderContact)
				note("B CreateOrders: %v %s %v", err, st.Status, st.Delivery)
				note("B tasks: %v", executeTasks(ctx, e))
			},
			func() {
				_, err := e.service.RemoveGroup(ctx, buyerID, created.ID, "t3")
				note("RemoveGroup t3: %v", err)
			},
			func() { note("tasks: %v", executeTasks(ctx, e)) },
		}
		rng.Shuffle(len(ops), func(i, j int) { ops[i], ops[j] = ops[j], ops[i] })
		var wg sync.WaitGroup
		for _, op := range ops {
			wg.Add(1)
			go func(op func(), delay time.Duration) {
				defer wg.Done()
				time.Sleep(delay)
				op()
			}(op, time.Duration(rng.Intn(300))*time.Microsecond)
		}
		wg.Wait()
		for range 4 {
			e.clock.Advance(16 * time.Second)
			if err := resumeAndExecute(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		_ = e.service.ConfirmPayment(ctx, buyerID, created.ID)
		final, _ := e.service.Get(ctx, buyerID, created.ID)
		stored, _ := e.store.Checkout(created.ID)
		problems := paymentConsentInvariants(ctx, e, final, stored)
		for _, tenant := range []ids.TenantID{"t1", "t2", "t3"} {
			requests := e.platform.OrderRequests(tenant)
			unique := map[string]bool{}
			for _, request := range requests {
				unique[request.ExternalCheckoutID] = true
				if final.Delivery == nil || request.PickupPointCode != final.Delivery.PickupPointCode {
					problem := string(tenant) + ": ПВЗ заказа " + request.PickupPointCode + " отличается от покупки"
					problems = append(problems, problem)
				}
			}
			if len(unique) > 1 {
				problems = append(problems, string(tenant)+": больше одного заказа")
			}
		}
		point := "-"
		if final.Delivery != nil {
			point = final.Delivery.PickupPointCode
		}
		outcomes[fmt.Sprintf(
			"%s %s заказов=%d",
			final.Status,
			point,
			len(e.platform.OrderRequests("t1"))+len(e.platform.OrderRequests("t2"))+len(e.platform.OrderRequests("t3")),
		)]++
		if len(problems) > 0 {
			violations++
			if violations <= 5 {
				logPaymentState(t, fmt.Sprintf("seed %d нарушения %v", seed, problems), final, nil)
				for _, event := range events {
					t.Logf("      %s", event)
				}
				for _, tenant := range []ids.TenantID{"t1", "t2", "t3"} {
					for _, request := range e.platform.OrderRequests(tenant) {
						t.Logf("      заказ %s: %s", tenant, request.PickupPointCode)
					}
				}
			}
		}
	}
	for outcome, count := range outcomes {
		t.Logf("исход %4d× %s", count, outcome)
	}
	t.Logf("прогонов: 300; с нарушениями: %d", violations)
	if violations > 0 {
		t.Fatalf("нарушены инварианты новой модели")
	}
}

func (p *claimedCreationGate) CreateOrder(
	ctx context.Context, tenant ids.TenantID, request ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	if tenant == "t1" {
		close(p.started)
		<-p.release
	}
	return p.PlatformAPI.CreateOrder(ctx, tenant, request)
}

// То же окно с запросом создания t1, отправленным магазину на MSK1 до сохранения выбора MSK2.
func TestPickupChangeIsRefusedWhileClaimedCreationIsInFlight(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	addConsentThirdShop(t, e)
	addAlternativePickup(t, e, "MSK2", "RU-MOW")
	created, err := e.service.Create(ctx, buyerID, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Quote(ctx, buyerID, created.ID, "cdek", "MSK1", "Город"); err != nil {
		t.Fatal(err)
	}
	blocking := &claimedCreationGate{
		PlatformAPI: e.platform,
		started:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	worker := appcheckout.NewService(
		e.store,
		blocking,
		e.pickups,
		e.verifier,
		&fake.IDs{},
		e.clock,
		appcheckout.Config{
			QuoteTTL:      30 * time.Minute,
			QuoteTimeout:  time.Second,
			PaymentStub:   true,
			ReturnBaseURL: "https://market.example",
		},
	)
	done := make(chan struct{})
	hook := &quoteBeforeCreationHook{PlatformAPI: e.platform}
	hook.during = func() {
		_, _ = e.service.Confirm(ctx, buyerID, created.ID)
		_, _ = e.service.CreateOrders(ctx, buyerID, created.ID, orderContact)
		tasks, err := fake.TasksOf(e.store.Claim(
			ctx,
			ports.ClaimRequest{
				Now:        e.clock.Now(),
				Limit:      10,
				PerTenant:  10,
				LeaseUntil: e.clock.Now().Add(6 * time.Second),
				Kinds:      []string{appcheckout.TaskCreateOrder},
			},
		))
		if err != nil {
			t.Error(err)
		}
		go func() {
			defer close(done)
			for _, task := range tasks {
				if task.TenantID == "t1" {
					_ = worker.HandleOrderTask(ctx, task)
				}
			}
		}()
		<-blocking.started
		_, _ = e.service.RemoveGroup(ctx, buyerID, created.ID, "t3")
	}
	secondTab := appcheckout.NewService(
		e.store,
		hook,
		e.pickups,
		e.verifier,
		&fake.IDs{},
		e.clock,
		appcheckout.Config{
			QuoteTTL:      30 * time.Minute,
			QuoteTimeout:  time.Second,
			PaymentStub:   true,
			ReturnBaseURL: "https://market.example",
		},
	)
	changed, changeErr := secondTab.Quote(ctx, buyerID, created.ID, "cdek", "MSK2", "Город")
	close(blocking.release)
	<-done
	if !errors.Is(changeErr, checkout.ErrSnapshotChanged) {
		t.Fatal("пункт меняется после начала создания", changeErr)
	}
	logPaymentState(t, "выбор MSK2 сохранён, пока создание t1 на MSK1 шло", changed, changeErr)
	mid, _ := e.service.Get(ctx, buyerID, created.ID)
	logPaymentState(t, "после ответа магазина t1", mid, nil)
	_, confirmErr := e.service.Confirm(ctx, buyerID, created.ID)
	_, orderErr := beginAndExecute(ctx, e, buyerID, created.ID, orderContact)
	for range 2 {
		e.clock.Advance(16 * time.Second)
		if err := resumeAndExecute(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	payErr := e.service.ConfirmPayment(ctx, buyerID, created.ID)
	final, _ := e.service.Get(ctx, buyerID, created.ID)
	logPaymentState(t, "итог", final, payErr)
	t.Logf("Confirm=%v CreateOrders=%v оплаты=%v", confirmErr, orderErr, e.platform.StubPaymentCalls())
	bad := false
	for _, tenant := range []ids.TenantID{"t1", "t2"} {
		for _, request := range e.platform.OrderRequests(tenant) {
			t.Logf(
				"заказ %s: пункт %s (пункт покупки %s)",
				tenant,
				request.PickupPointCode,
				final.Delivery.PickupPointCode,
			)
			bad = bad || request.PickupPointCode != final.Delivery.PickupPointCode
		}
	}
	if bad {
		t.Fatalf("заказ магазина отправлен на другой пункт, чем пункт покупки")
	}
}

func TestRemovingExpiredUnreachableCreationPreservesHealthyOrder(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	// Сначала магазин t1 перестал отвечать на создание; расчёт ещё проходил.
	e.platform.FailCreate("t1", errors.New("timeout"))
	state, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	logPaymentState(t, "после CreateOrders", state, err)
	e.clock.Advance(appcheckout.CreationTimeout + time.Second)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	state, _ = e.service.Get(ctx, buyerID, purchase.ID)
	logPaymentState(t, "после окна создания", state, nil)
	// Теперь сервер магазина t1 лежит целиком (расчёт тоже).
	e.platform.Fail("t1", errors.New("timeout"))
	for round := range 3 {
		confirmed, confirmErr := e.service.Confirm(ctx, buyerID, purchase.ID)
		logPaymentState(t, "Confirm", confirmed, confirmErr)
		ordered, ordersErr := e.service.CreateOrders(ctx, buyerID, purchase.ID, orderContact)
		logPaymentState(t, "CreateOrders", ordered, ordersErr)
		_, payErr := e.service.Pay(ctx, buyerID, purchase.ID)
		t.Logf("раунд %d: Pay=%v", round, payErr)
		if err := executeTasks(ctx, e); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Minute)
	}
	_, quoteErr := e.service.Quote(ctx, buyerID, purchase.ID, "cdek", "MSK1", "Город")
	final, _ := e.service.Get(ctx, buyerID, purchase.ID)
	logPaymentState(t, "итог", final, nil)
	t.Logf("сменить пункт t1: %v; оплат: %d", quoteErr, len(e.platform.StubPaymentCalls()))
	// Решение владельца: недоступный магазин не исключается сам; выход — явное удаление покупателем.
	if len(e.platform.StubPaymentCalls()) != 0 || final.Groups[1].Status != checkout.GroupAwaitingPayment ||
		len(e.platform.CancellationRequests("t2")) != 0 {
		t.Fatalf("до решения покупателя заказ t2 должен сохраняться без оплаты и без отмены")
	}
	if _, err := e.service.RemoveGroup(ctx, buyerID, purchase.ID, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Confirm(ctx, buyerID, purchase.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, purchase.ID); err != nil ||
		len(e.platform.StubPaymentCalls()) != 1 || e.platform.StubPaymentCalls()[0] != "t2" {
		t.Fatalf("после удаления t1 оплачивается только t2: %v %v", err, e.platform.StubPaymentCalls())
	}
}

func (p *differentOrderPickup) CreateOrder(
	ctx context.Context, tenant ids.TenantID, request ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	if tenant == "t1" {
		request.PickupPointCode = "MSK2"
	}
	return p.PlatformAPI.CreateOrder(ctx, tenant, request)
}

func TestOrderWithDifferentPickupIsCancelledBeforePayment(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	e.service = appcheckout.NewService(
		e.store,
		&differentOrderPickup{e.platform},
		e.pickups,
		e.verifier,
		&fake.IDs{},
		e.clock,
		appcheckout.Config{
			QuoteTTL:      time.Hour,
			QuoteTimeout:  time.Second,
			PaymentStub:   true,
			ReturnBaseURL: "https://market.example",
		},
	)
	state, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != checkout.StatusDraft || !state.Groups[0].Excluded() || state.Groups[0].Quote != nil {
		t.Fatal("заказ с другим ПВЗ требует проверки состава", state)
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, purchase.ID); err == nil {
		t.Fatal("оплата до нового согласия")
	}
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	if len(e.platform.CancellationRequests("t1")) != 1 {
		t.Fatal("заказ на другой ПВЗ должен отмениться по ключу", e.platform.CancellationRequests("t1"))
	}
	if _, err := e.service.Confirm(ctx, buyerID, purchase.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, purchase.ID); err != nil {
		t.Fatal(err)
	}
	calls := e.platform.StubPaymentCalls()
	if len(calls) != 1 || calls[0] != "t2" || len(e.platform.OrderRequests("t2")) != 1 {
		t.Fatal("оплачивается только здоровый заказ, без дубликатов", calls)
	}
}

func (p *quoteBeforeCreationHook) QuoteDelivery(
	ctx context.Context, tenant ids.TenantID, request ports.QuoteRequest,
) (delivery.Offered, error) {
	if request.PickupPointCode == "MSK2" {
		p.once.Do(p.during)
	}
	return p.PlatformAPI.QuoteDelivery(ctx, tenant, request)
}
