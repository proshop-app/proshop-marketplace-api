package checkout

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

func TestReconcileShopsKeepsShopTogetherAndBatchUnderContractLimit(t *testing.T) {
	var entries []checkout.ReconcileEntry
	for range 2*ReconcileBatch + 50 {
		entries = append(entries, checkout.ReconcileEntry{TenantID: "t1"})
	}
	for range 3 {
		entries = append(entries, checkout.ReconcileEntry{TenantID: "t2"})
	}
	shops := reconcileShops(entries)
	if len(shops) != 2 || len(shops[0]) != 3 || len(shops[1]) != 1 {
		t.Fatalf("магазин — одна очередь запросов: %d", len(shops))
	}
	sizes := []int{len(shops[0][0].entries), len(shops[0][1].entries), len(shops[0][2].entries)}
	if sizes[0] != ReconcileBatch || sizes[1] != ReconcileBatch || sizes[2] != 50 || len(shops[1][0].entries) != 3 {
		t.Fatalf("размеры пачек: %v", sizes)
	}
}
