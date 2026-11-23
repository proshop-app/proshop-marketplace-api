package platformapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"sync/atomic"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestReconcileTwoHundredMismatchesUsesOneHTTPCall(t *testing.T) {
	const (
		size   = 200
		reason = checkout.CancelledBeforeShipment
		link   = "https://example.test/track"
	)
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	store := fake.NewStore()
	identities := &fake.IDs{}
	purchases := make([]ids.UUID, 0, size)
	answers := make(map[string]map[string]any, size)
	if err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for i := range size {
			id := identities.NewUUID()
			orderID := fmt.Sprintf("o%d", i)
			state := checkout.State{
				ID:             id,
				IdempotencyKey: string(id),
				Status:         checkout.StatusPaid,
				Groups: []checkout.Group{
					{
						TenantID:        "t1",
						OrderID:         orderID,
						Status:          checkout.GroupPaid,
						LastOrderStatus: checkout.GroupPaid,
						OrderStatusAt:   now,
					},
				},
			}
			if err := tx.Checkouts().Add(ctx, checkout.Restore(state)); err != nil {
				return err
			}
			purchases = append(purchases, id)
			answer := map[string]any{
				"orderId":  orderID,
				"revision": 1,
			}
			if i%2 == 0 {
				answer["status"] = checkout.GroupReturned
				answer["reason"] = reason
			} else {
				answer["status"] = checkout.GroupShipped
				answer["tracking"] = map[string]string{
					"carrier": "carrier",
					"url":     link,
				}
			}
			answers[orderID] = answer
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(answers[path.Base(r.URL.Path)])
			return
		}
		var body struct {
			OrderIDs []string `json:"orderIds"`
		}
		if r.Method != http.MethodPost || path.Base(r.URL.Path) != "statuses" ||
			json.NewDecoder(r.Body).Decode(&body) != nil || len(body.OrderIDs) != size {
			http.Error(w, "ожидалась пачка из 200 заказов", http.StatusBadRequest)
			return
		}
		items := make([]map[string]any, 0, size)
		for _, id := range body.OrderIDs {
			items = append(items, answers[id])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	})
	client := New(Config{
		BaseURL: "https://shop.example.test",
		Token:   "service-token",
		Timeout: time.Second,
		Conns:   4,
	})
	client.http.Transport = handlerTransport(handler)
	service := appcheckout.NewService(store, client, nil, nil, identities, fake.NewClock(now), appcheckout.Config{})

	result, err := service.ReconcileOrders(ctx)
	if err != nil || result.Checked != size || result.Mismatched != size || calls.Load() != 1 {
		t.Fatalf("итог=%+v, HTTP=%d, ошибка=%v", result, calls.Load(), err)
	}
	for i, id := range purchases {
		state, err := service.Get(ctx, "", id)
		if err != nil {
			t.Fatal(err)
		}
		group := state.Groups[0]
		if i%2 == 0 && (group.Status != checkout.GroupReturned || group.Reason != reason) {
			t.Fatalf("потеряна причина возврата: %+v", group)
		}
		if i%2 != 0 && (group.Status != checkout.GroupShipped || group.Tracking == nil || group.Tracking.URL != link) {
			t.Fatalf("потеряно отслеживание отправления: %+v", group)
		}
	}
}
