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
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type delayedConsentPayment struct {
	ports.PlatformAPI
	mu      sync.Mutex
	pending []func()
	lost    map[ids.TenantID]bool
}

func (p *delayedConsentPayment) StubPayment(
	ctx context.Context,
	tenant ids.TenantID,
	id string,
	amount money.Money,
) error {
	p.mu.Lock()
	lose := p.lost[tenant]
	if lose {
		delete(p.lost, tenant)
		p.pending = append(p.pending, func() {
			_ = p.PlatformAPI.StubPayment(context.Background(), tenant, id, amount)
		})
	}
	p.mu.Unlock()
	if lose {
		return errors.New("context deadline exceeded")
	}
	return p.PlatformAPI.StubPayment(ctx, tenant, id, amount)
}

func (p *delayedConsentPayment) commit() {
	p.mu.Lock()
	pending := p.pending
	p.pending = nil
	p.mu.Unlock()
	for _, fn := range pending {
		fn()
	}
}

// Хаос: частичная оплата, затем параллельно оплата (с потерянным ответом t2), удаление t2/t3,
// отмена, подтверждение, сверка — в случайном порядке; затем поздняя фиксация и дренаж задач.
func TestConcurrentPaymentAndCompositionChangesPreservePaidOrders(t *testing.T) {
	violations := 0
	outcomes := map[string]int{}
	defer func() {
		for outcome, count := range outcomes {
			t.Logf("исход %4d× %s", count, outcome)
		}
	}()
	for seed := int64(0); seed < 300; seed++ {
		e := setup(t)
		ctx := context.Background()
		state := partiallyPaidConsentPurchase(t, e)
		rng := rand.New(rand.NewSource(seed))
		late := &delayedConsentPayment{PlatformAPI: e.platform, lost: map[ids.TenantID]bool{}}
		if rng.Intn(2) == 0 {
			late.lost["t2"] = true
		}
		switch rng.Intn(4) {
		case 1:
			e.store.SeedVerdict(moderation.VerdictState{TenantID: "t2", ProductID: "p1", Status: moderation.Pending})
		case 2:
			e.store.SeedVerdict(moderation.VerdictState{TenantID: "t2", ProductID: "p1", Status: moderation.Rejected})
		case 3:
			seller, _ := e.store.Participant("t3")
			seller.Stage = participant.StageDisconnected
			e.store.SeedParticipant(seller)
		}
		payer := appcheckout.NewService(
			e.store,
			late,
			e.pickups,
			e.verifier,
			&fake.IDs{},
			e.clock,

			appcheckout.Config{
				QuoteTTL:     30 * time.Minute,
				QuoteTimeout: time.Second,
				PaymentStub:  true,

				ReturnBaseURL: "https://market.example",
			},
		)
		ops := []func(){

			func() {
				_ = payer.ConfirmPayment(ctx, buyerID, state.ID)
			},

			func() {
				_, _ = e.service.RemoveGroup(ctx, buyerID, state.ID, "t2")
			},

			func() {
				_, _ = e.service.RemoveGroup(ctx, buyerID, state.ID, "t3")
			},

			func() {
				_, _ = e.service.Cancel(ctx, buyerID, state.ID)
			},

			func() {
				_, _ = e.service.Confirm(ctx, buyerID, state.ID)
			},

			func() {
				_ = resumeAndExecute(ctx, e)
			},

			func() {
				_ = payer.ConfirmPayment(ctx, buyerID, state.ID)
			},

			func() {
				_, _ = e.service.Pay(ctx, buyerID, state.ID)
			},

			func() {
				_, _ = e.service.Get(ctx, buyerID, state.ID)
			},
		}
		rng.Shuffle(len(ops), func(i, j int) {
			ops[i], ops[j] = ops[j], ops[i]
		})
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
		late.commit()
		for range 6 {
			e.clock.Advance(16 * time.Minute)
			if err := resumeAndExecute(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		final, _ := e.service.Get(ctx, buyerID, state.ID)
		stored, _ := e.store.Checkout(state.ID)
		problems := paymentConsentInvariants(ctx, e, final, stored)
		outcome := fmt.Sprintf("lost=%v %s", late.lost["t2"] || len(late.pending) > 0, final.Status)
		for _, g := range final.Groups {
			outcome += " " + string(g.TenantID) + "=" + string(g.Status)
		}
		outcomes[outcome]++
		if len(problems) > 0 {
			violations++
			if violations <= 5 {
				logPaymentState(t, fmt.Sprintf("seed %d нарушения %v", seed, problems), final, nil)
				t.Logf(
					"   PaymentStarted=%v оплаты=%v отмены t1=%d t2=%d t3=%d корзина=%s",
					stored.PaymentStarted,

					e.platform.StubPaymentCalls(),
					len(e.platform.CancellationRequests("t1")),

					len(
						e.platform.CancellationRequests(
							"t2",
						),
					),
					len(
						e.platform.CancellationRequests(
							"t3",
						),
					),
					paymentCartContents(
						t,
						e,
					),
				)
			}
		}
	}
	t.Logf("прогонов: 300; с нарушениями: %d", violations)
	if violations > 0 {
		t.Fatalf("нарушены денежные инварианты")
	}
}

func paymentConsentInvariants(ctx context.Context, e *env, final, stored checkout.State) []string {
	var problems []string
	paidAny := false
	for _, g := range final.Groups {
		if g.OrderID == "" {
			continue
		}
		order, err := e.platform.GetOrder(ctx, g.TenantID, g.OrderID)
		if err != nil {
			continue
		}
		serverPaid := order.Status == checkout.GroupPaid || order.Status == checkout.GroupShipped
		if serverPaid {
			paidAny = true
		}
		if serverPaid && !g.Paid() {
			problems = append(problems, string(g.TenantID)+": сервер paid, площадка нет")
		}
		if serverPaid && len(e.platform.CancellationRequests(g.TenantID)) > 0 {
			problems = append(problems, string(g.TenantID)+": отмена отправлена оплаченному")
		}
		if !serverPaid && g.Paid() {
			problems = append(problems, string(g.TenantID)+": площадка paid, сервер нет")
		}
	}
	if paidAny && final.Status != checkout.StatusPaid && final.Status != checkout.StatusPartiallyPaid {
		problems = append(problems, "есть оплата, статус "+string(final.Status))
	}
	if stored.PaymentStarted {
		problems = append(problems, "PaymentStarted после дренажа")
	}
	for _, g := range final.Groups {
		if g.ReconciliationRequired {
			problems = append(problems, string(g.TenantID)+": reconcile после дренажа")
		}
	}
	return problems
}

// Возврат товара отменённого магазином заказа в корзину: без оплаченных соседей и с оплаченным t1.
func TestShopCancellationReturnsGoodsOnceAndRequiresConfirmation(t *testing.T) {
	for _, withPaid := range []bool{false, true} {
		e := setup(t)
		ctx := context.Background()
		addConsentThirdShop(t, e)
		purchase := confirmedPurchase(t, e)
		state, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
		if err != nil {
			t.Fatal(err)
		}
		if withPaid {
			e.platform.SetOrderStatus(state.Groups[0].OrderID, checkout.GroupPaid, "")
			if err := announceOrder(ctx, e, state.Groups[0].TenantID, state.ID, state.Groups[0].OrderID); err != nil {
				t.Fatal(err)
			}
		}
		e.platform.SetOrderStatus(state.Groups[1].OrderID, checkout.GroupCancelled, "shop_cancelled")
		if err := announceOrder(ctx, e, state.Groups[1].TenantID, state.ID, state.Groups[1].OrderID); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			e.clock.Advance(time.Minute)
			if err := resumeAndExecute(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		after, _ := e.service.Get(ctx, buyerID, purchase.ID)
		logPaymentState(t, fmt.Sprintf("оплачен t1=%v, t2 отменён магазином", withPaid), after, nil)
		if !checkout.Restore(after).NeedsConfirmation() || !after.Groups[1].CartReturned ||
			paymentCartContents(t, e) != "t2~p1×1 " {
			t.Fatal("отмена должна вернуть товары один раз и потребовать согласие", after)
		}
		if withPaid && after.Status != checkout.StatusPartiallyPaid {
			t.Fatal("состоявшаяся оплата должна сохраняться", after.Status)
		}
	}
}
