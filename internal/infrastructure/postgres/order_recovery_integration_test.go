//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestOrderRetryAndCartRemainderPersistAcrossRestart(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	seedBuyer(t, uow, buyerA, "79251234567")
	line := checkout.Line{
		Offer:    ids.OfferID{Tenant: "t1", Product: "p1"},
		Quantity: 1,
		Name:     "Товар",
		Price:    money.FromKopecks(10000),
	}
	c, err := checkout.New(buyerB, buyerA, "recovery-persistence", []checkout.Line{line}, at(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SelectDelivery(
		checkout.Selection{Carrier: "cdek", PickupPointCode: "P1"},
		map[ids.TenantID]delivery.Quote{"t1": {Status: delivery.QuoteAvailable, ExpiresAt: at(60)}},
		at(0),
	); err != nil {
		t.Fatal(err)
	}
	if err := c.Confirm(at(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BeginOrders(
		checkout.Contact{Phone: "79251234567", Name: "Покупатель"},
		at(0),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MarkCartConsumed(at(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClaimCreation("t1", "create", at(0).Add(5*time.Second+100*time.Millisecond), at(0)); err != nil {
		t.Fatal(err)
	}
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, c)
	}); err != nil {
		t.Fatal(err)
	}
	now := at(0)
	err = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		loaded, err := tx.Checkouts().Get(ctx, buyerB)
		if err != nil {
			return err
		}
		if claimed, err := loaded.ClaimCreation(
			"t1",
			"duplicate",
			now.Add(5*time.Second+100*time.Millisecond),
			now,
		); err != nil || claimed {
			return fmt.Errorf("сохранённая аренда должна пережить чтение: %v %w", claimed, err)
		}
		_, err = loaded.CompleteCreation(
			"t1",
			"create",
			checkout.SellerOrder{
				Carrier:         "cdek",
				PickupPointCode: "P1",
				OrderID:         "o1",
				Total:           money.FromKopecks(10000),
				ExpiresAt:       at(0).Add(checkout.PaymentWindow),
			},
			now,
		)
		if err != nil {
			return err
		}
		now = loaded.Groups()[0].PaymentDeadline().Add(checkout.PaymentExpiryGrace)
		for range checkout.MaxOrderFailures {
			claimed, err := loaded.ClaimPoll("t1", "poll", now)
			if err != nil {
				return err
			}
			if !claimed {
				return errors.New("опрос не захвачен")
			}
			if _, err := loaded.FailPoll("t1", "poll", "invalid_status", now); err != nil {
				return err
			}
			now = loaded.Groups()[0].NextPollAt
		}
		return tx.Checkouts().Update(ctx, loaded)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		loaded, err := tx.Checkouts().Get(ctx, buyerB)
		if err != nil {
			return err
		}
		g := loaded.Groups()[0]
		if g.Status != checkout.GroupSellerUnreachable || g.PollFailures != checkout.MaxOrderFailures ||
			!g.NextPollAt.Equal(now) || g.LastOrderStatus != checkout.GroupAwaitingPayment {
			return fmt.Errorf("счётчик и исходный статус потеряны: %+v", g)
		}
		if _, err := loaded.Observe(
			"t1",
			checkout.SellerOrder{
				Carrier:         "cdek",
				PickupPointCode: "P1",
				OrderID:         "o1",
				Status:          checkout.GroupCancelled,
				Reason:          "payment_expired",
			},
			now,
		); err != nil {
			return err
		}
		if _, err := loaded.RecordCartReturn("t1", []checkout.Line{line}, "cart.invalid_quantity", now); err != nil {
			return err
		}
		return tx.Checkouts().Update(ctx, loaded)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		loaded, err := tx.Checkouts().Get(ctx, buyerB)
		if err != nil {
			return err
		}
		g := loaded.Groups()[0]
		if !g.ReturnFailed || g.CartReturned || len(g.ReturnRemaining) != 1 ||
			!reflect.DeepEqual(g.ReturnRemaining[0], line) {
			return fmt.Errorf("остаток возврата потерян: %+v", g)
		}
		waiting, err := tx.Checkouts().LockWorkDue(ctx, now, checkout.WorkCursor{}, 100)
		if err != nil || len(waiting.Due) != 0 {
			return fmt.Errorf("рано повторять возврат: %d %w", len(waiting.Due), err)
		}
		ready, err := tx.Checkouts().LockWorkDue(ctx, now.Add(time.Minute), checkout.WorkCursor{}, 100)
		if err != nil || len(ready.Due) != 1 {
			return fmt.Errorf("пора вернуть остаток: %d %w", len(ready.Due), err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExhaustedCreationReturnDueAfterRestart(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	seedBuyer(t, uow, buyerA, "79251234567")
	line := checkout.Line{
		Offer:    ids.OfferID{Tenant: "t1", Product: "p1"},
		Quantity: 1,
		Name:     "Товар",
		Price:    money.FromKopecks(10000),
	}
	c, err := checkout.New(buyerB, buyerA, "exhausted-persistence", []checkout.Line{line}, at(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SelectDelivery(
		checkout.Selection{Carrier: "cdek", PickupPointCode: "P1"},
		map[ids.TenantID]delivery.Quote{"t1": {Status: delivery.QuoteAvailable, ExpiresAt: at(60)}},
		at(0),
	); err != nil {
		t.Fatal(err)
	}
	if err := c.Confirm(at(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BeginOrders(
		checkout.Contact{Phone: "79251234567", Name: "Покупатель"},
		at(0),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MarkCartConsumed(at(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClaimCreation("t1", "create", at(0).Add(5*time.Second+100*time.Millisecond), at(0)); err != nil {
		t.Fatal(err)
	}

	if _, err := c.FailCreation(
		"t1",
		"create",
		"IDEMPOTENCY_CONFLICT",
		"отказ",
		checkout.GroupSellerUnreachable,
		true,
		nil,
		at(0),
	); err != nil {
		t.Fatal(err)
	}
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, c)
	}); err != nil {
		t.Fatal(err)
	}
	for _, returned := range []bool{false, true} {
		if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
			page, err := tx.Checkouts().LockWorkDue(ctx, at(0), checkout.WorkCursor{}, 100)
			if err != nil {
				return err
			}
			due := page.Due
			if returned {
				if len(due) != 0 {
					return fmt.Errorf("возвращённая группа снова выбрана")
				}
				return nil
			}
			if len(due) != 1 {
				return fmt.Errorf("исчерпанная группа не выбрана: %d", len(due))
			}
			if _, err := due[0].RecordCartReturn("t1", nil, "", at(0)); err != nil {
				return err
			}
			return tx.Checkouts().Update(ctx, due[0])
		}); err != nil {
			t.Fatal(err)
		}
	}
}
