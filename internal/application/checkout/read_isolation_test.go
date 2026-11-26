package checkout_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type checkoutReadObserver struct {
	failures []checkout.ReadFailure
	logged   int
}

func (*checkoutReadObserver) RecordCheckoutEvent(ids.TenantID, ids.UUID, string) {}

func (o *checkoutReadObserver) RecordCheckoutReadFailure(
	_ context.Context,
	tenant ids.TenantID,
	id ids.UUID,
	cause error,
	log bool,
) {
	o.failures = append(o.failures, checkout.ReadFailure{TenantID: tenant, ID: id, Cause: cause})
	if log {
		o.logged++
	}
}

func TestCheckoutReadFailureIsolatedFromOtherPurchases(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	observer := &checkoutReadObserver{}
	e.service.SetObserver(observer)
	purchaseIDs := make([]ids.UUID, 101)
	err := e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for i := range purchaseIDs {
			purchaseIDs[i] = ids.UUID(fmt.Sprintf("%08x-0000-4000-8000-000000000000", i+1))
			state := checkout.State{
				ID:             purchaseIDs[i],
				BuyerID:        buyerID,
				IdempotencyKey: fmt.Sprint(i),
				Status:         checkout.StatusPaid,
				Groups:         []checkout.Group{{TenantID: "t1", OrderID: "order"}},
			}
			if err := tx.Checkouts().Add(ctx, checkout.Restore(state)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	broken := purchaseIDs[99]
	cause := errors.New("внутренняя причина разбора")
	e.store.FailCheckoutRead(broken, "t1", cause)

	operator, err := e.service.OperatorList(ctx, checkout.ListCursor{}, false)
	if err != nil || len(operator.Items) != 99 || operator.Next.ID != broken || len(operator.Issues) != 1 {
		t.Fatalf("страница оператора: %+v, %v", operator, err)
	}
	issue := operator.Issues[0]
	if issue.CheckoutID != broken || issue.TenantID != "t1" || issue.Reason != checkout.ReadGroup {
		t.Fatalf("признак неразобравшейся покупки: %+v", issue)
	}
	buyer, err := e.service.Purchases(ctx, buyerID, "")
	if err != nil || len(buyer.Items) != 99 || buyer.Next.ID != broken || len(buyer.Issues) != 0 {
		t.Fatalf("страница покупателя: %+v, %v", buyer, err)
	}
	next, err := e.service.Purchases(ctx, buyerID, buyer.Next.ID)
	if err != nil || len(next.Items) != 1 || next.Items[0].State().ID != purchaseIDs[100] {
		t.Fatalf("следующая страница покупателя: %+v, %v", next, err)
	}

	const anotherBuyer ids.UUID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	foreign, err := e.service.Purchases(ctx, anotherBuyer, "")
	if err != nil || len(foreign.Items) != 0 || foreign.Next.ID != "" {
		t.Fatalf("чужой кабинет: %+v, %v", foreign, err)
	}
	if _, err := e.service.Get(ctx, anotherBuyer, broken); !errors.Is(err, checkout.ErrNotFound) {
		t.Fatalf("чужая покупка: %v", err)
	}
	if _, err := e.service.Get(ctx, buyerID, broken); !errors.Is(err, checkout.ErrUnreadable) {
		t.Fatalf("ошибка собственной покупки: %v", err)
	}
	if len(observer.failures) != 3 {
		t.Fatalf("ожидались два чтения списка и чтение владельцем: %+v", observer.failures)
	}
	for _, event := range observer.failures {
		if event.ID != broken || event.TenantID != "t1" || !errors.Is(event.Cause, cause) {
			t.Fatalf("диагностика ошибки разбора: %+v", event)
		}
	}

	observer.failures, observer.logged = nil, 0
	for _, id := range purchaseIDs[:11] {
		e.store.FailCheckoutRead(id, "t1", cause)
	}
	operator, err = e.service.OperatorList(ctx, checkout.ListCursor{}, false)
	if err != nil || len(operator.Issues) != 12 || len(observer.failures) != 12 || observer.logged != 10 {
		t.Fatalf(
			"диагностика страницы: ошибок %d, событий %d, строк %d, %v",
			len(operator.Issues),
			len(observer.failures),
			observer.logged,
			err,
		)
	}
	invalid := checkout.State{
		ID:     purchaseIDs[12],
		Groups: []checkout.Group{{TenantID: "t1"}, {TenantID: "t2"}},
	}
	renderIssues := operator.ReportReadRenderingFailure(ctx, invalid, cause)
	if len(renderIssues) != 2 || len(observer.failures) != 13 || observer.logged != 10 {
		t.Fatalf(
			"диагностика преобразования: признаков %d, событий %d, строк %d",
			len(renderIssues),
			len(observer.failures),
			observer.logged,
		)
	}
}
