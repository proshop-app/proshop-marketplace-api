package checkout_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

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
	e.platform.BeforePickup = func(_ context.Context, _ ids.TenantID) error {
		if calls.Add(1) > 1 {
			return nil
		}
		close(started)
		<-release
		return nil
	}
	firstCtx, stopFirst := context.WithCancel(ctx)
	defer stopFirst()
	first := make(chan error, 1)
	go func() {
		_, quoteErr := e.service.Quote(firstCtx, buyerID, draft.ID, "cdek", "MSK1", "Первый")
		first <- quoteErr
	}()
	<-started
	stopFirst()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	refused := 0
	for i := 1; i < 40; i++ {
		_, quoteErr := e.service.Quote(ctx, buyerID, draft.ID, "cdek", "MSK1", fmt.Sprintf("Атака %d", i))
		if errors.Is(quoteErr, delivery.ErrSelectionBusy) {
			refused++
		}
	}
	_, pointErr := e.pickups.Point(ctx, "buyer-b", "cdek", "MSK1", "Холодный город покупателя Б")
	if pointErr != nil || calls.Load() != 2 || refused != 39 {
		t.Fatalf("расчёты заняли чужой резерв: point=%v calls=%d personalRefusals=%d", pointErr, calls.Load(), refused)
	}
}
