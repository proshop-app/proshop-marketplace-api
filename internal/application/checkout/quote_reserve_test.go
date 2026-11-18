package checkout_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Б1, вход через расчёт доставки: один покупатель с черновиком шлёт расчёты с новыми городами.
// Каждый расчёт входит в единый личный лимит Point, сохраняя резерв другим покупателям.
func TestQuoteStormLeavesPointReserveForOtherBuyers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	draft, err := e.service.Create(ctx, buyerID, key)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	e.platform.BeforePickup = func(ctx context.Context, _ ids.TenantID) error {
		if calls.Add(1) > 1 {
			return nil
		}
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	firstCtx, stopFirst := context.WithCancel(ctx)
	defer stopFirst()
	first := make(chan error, 1)
	go func() {
		_, quoteErr := e.service.Quote(firstCtx, buyerID, draft.ID, "cdek", "MSK1", "Первый")
		first <- quoteErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("первая загрузка не началась")
	}
	stopFirst()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	refused := 0
	for i := 1; i < 40; i++ {
		attack, cancel := context.WithTimeout(ctx, time.Second)
		_, quoteErr := e.service.Quote(attack, buyerID, draft.ID, "cdek", "MSK1", fmt.Sprintf("Атака %d", i))
		cancel()
		if errors.Is(quoteErr, delivery.ErrSelectionBusy) {
			refused++
		}
	}
	victim, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	start := time.Now()
	_, pointErr := e.pickups.Point(victim, "buyer-b", "cdek", "MSK1", "Холодный город покупателя Б")
	t.Logf(
		"после 40 расчётов: Point другого покупателя=%v за %s; вызовов магазинов=%d",
		pointErr,
		time.Since(start),
		calls.Load(),
	)
	if pointErr != nil || calls.Load() != 2 || refused != 39 {
		t.Fatalf("расчёты заняли чужой резерв: point=%v calls=%d personalRefusals=%d", pointErr, calls.Load(), refused)
	}
}
