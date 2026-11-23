//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	checkoutNumberParallel  = 16
	checkoutNumberStart     = 10_000_000
	overwriteCheckoutNumber = `UPDATE checkouts SET number = 1 WHERE id = $1`
)

// Параллельные оформления получают разные номера из последовательности базы в самой вставке;
// повтор по ключу идемпотентности не выдаёт покупке второй номер, обновление его не трогает.
func TestCheckoutNumberIsIssuedOnceByDatabase(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedBuyer(t, uow, buyerA, "79251234567")

	purchases := make([]*checkout.Checkout, checkoutNumberParallel)
	for i := range purchases {
		purchases[i] = newNumberCheckout(t, i, fmt.Sprintf("number-key-%d", i))
	}

	errs := make([]error, len(purchases))
	var wg sync.WaitGroup
	for i, purchase := range purchases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
				return tx.Checkouts().Add(ctx, purchase)
			})
		}()
	}
	wg.Wait()

	seen := make(map[int64]bool, len(purchases))
	for i, purchase := range purchases {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		number := purchase.State().Number
		if number < checkoutNumberStart || seen[number] {
			t.Fatalf("покупка %d: номер %d выдан повторно или вне последовательности", i, number)
		}
		seen[number] = true
	}

	first := purchases[0].State()
	again := newNumberCheckout(t, len(purchases), first.IdempotencyKey)
	err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, again)
	})
	if !errors.Is(err, checkout.ErrDuplicateKey) {
		t.Fatalf("повтор по ключу: %v", err)
	}

	err = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		stored, err := tx.Checkouts().Get(ctx, first.ID)
		if err != nil {
			return err
		}
		if _, err := stored.SetPromoCodes(nil, at(1)); err != nil {
			return err
		}
		return tx.Checkouts().Update(ctx, stored)
	})
	if err != nil {
		t.Fatal(err)
	}

	err = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		stored, err := tx.Checkouts().Get(ctx, first.ID)
		if err != nil {
			return err
		}
		if stored.State().Number != first.Number {
			return fmt.Errorf("номер после повтора и обновления: %d, ждали %d", stored.State().Number, first.Number)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(context.Background(), overwriteCheckoutNumber, string(first.ID)); err == nil {
		t.Fatal("база позволила переписать номер покупки")
	}
}

func newNumberCheckout(t *testing.T, i int, key string) *checkout.Checkout {
	t.Helper()

	id := ids.UUID(fmt.Sprintf("00000000-0000-0000-0000-%012d", i))
	purchase, err := checkout.New(
		id,
		buyerA,
		key,
		[]checkout.Line{
			{
				Offer:    ids.OfferID{Tenant: "t1", Product: "p1"},
				Name:     "Товар",
				Quantity: 1,
				Price:    money.FromKopecks(100),
			},
		},
		at(0),
	)
	if err != nil {
		t.Fatal(err)
	}

	return purchase
}
