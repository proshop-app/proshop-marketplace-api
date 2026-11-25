package checkout

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// slowStatuses — магазин, не укладывающийся в срок чтения на пачке больше limit заказов.
type slowStatuses struct {
	ports.PlatformAPI
	limit int
	sizes []int
}

func (p *slowStatuses) OrderStatuses(
	_ context.Context,
	_ ids.TenantID,
	orderIDs []string,
) ([]checkout.OrderStatus, error) {
	p.sizes = append(p.sizes, len(orderIDs))
	if len(orderIDs) > p.limit {
		return nil, context.DeadlineExceeded
	}
	statuses := make([]checkout.OrderStatus, 0, len(orderIDs))
	for _, id := range orderIDs {
		statuses = append(statuses, checkout.OrderStatus{OrderID: id, Status: checkout.GroupPaid, Revision: 1})
	}
	return statuses, nil
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

// Магазин отвечает в срок только на пачках до 50 заказов: проход уменьшает пачку и сверяет всё.
func TestReconcileShopShrinksBatchUntilShopAnswers(t *testing.T) {
	platform := &slowStatuses{limit: 50}
	service := NewService(nil, platform, nil, nil, nil, nil, Config{ReconcileRetryPause: time.Millisecond})
	entries := make([]checkout.ReconcileEntry, 0, ReconcileBatch)
	for i := range ReconcileBatch {
		entries = append(entries, checkout.ReconcileEntry{
			TenantID: "t1",
			OrderID:  fmt.Sprintf("o%d", i),
			Status:   checkout.GroupPaid,
			Revision: 1,
		})
	}
	run := &reconcileRun{result: ReconcileResult{Failed: map[ids.TenantID]error{}}}
	service.reconcileShop(context.Background(), entries, run)
	if len(run.result.Failed) != 0 || run.result.Checked != ReconcileBatch {
		t.Fatalf("все заказы магазина сверены: %+v", run.result)
	}
	if !slices.Equal(platform.sizes, []int{200, 100, 50, 50, 50, 50}) {
		t.Fatalf("размеры запросов: %v", platform.sizes)
	}
}
