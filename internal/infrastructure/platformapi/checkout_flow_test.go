package platformapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/platformguard"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type pickupCheckoutServer struct {
	mu             sync.Mutex
	tariffs        map[string]money.Money
	platform       *fake.Platform
	quote          delivery.Offered
	points         ports.PickupPoints
	redispatch     bool
	dispatchReview *ports.DispatchReview
}

func (s *pickupCheckoutServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	tenant := ids.TenantID("")
	if len(parts) > 5 && parts[4] == "tenants" {
		tenant = ids.TenantID(parts[5])
	}
	var result any
	var err error
	switch {
	case strings.HasSuffix(r.URL.Path, "/items/check"):
		result = map[string]any{
			"promoCode": nil,
			"items": []any{
				map[string]any{
					"productId":         "p1",
					"available":         true,
					"availableQuantity": 3,
					"price":             199000,
					"discounts":         []any{},
				},
			},
		}
	case strings.HasSuffix(r.URL.Path, "/delivery/pickup-points"):
		answer := s.points
		answer.Items = append([]delivery.PickupPoint(nil), answer.Items...)
		for i := range answer.Items {
			answer.Items[i].Carrier = delivery.Carrier(r.URL.Query().Get("carrier"))
		}
		result = answer
	case strings.HasSuffix(r.URL.Path, "/delivery/quote"):
		var body struct{ DispatchReview *ports.DispatchReview }
		err = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		answer := s.quote
		if s.redispatch && tenant == "t1" && body.DispatchReview != nil {
			s.dispatchReview = body.DispatchReview
			answer.MinDays, answer.MaxDays = 7, 9
		}
		s.mu.Unlock()
		result = answer
	case strings.HasSuffix(r.URL.Path, "/orders/cancel-by-checkout"):
		var body ports.CancelOrderRequest
		err = json.NewDecoder(r.Body).Decode(&body)
		if err == nil {
			var cancelled ports.CancelOrderResult
			cancelled, err = s.platform.CancelOrder(ctx, tenant, body)
			result = map[string]any{
				"status":  "cancelled",
				"orderId": cancelled.OrderID,
			}
		}
	case strings.HasSuffix(r.URL.Path, "/stub-payment"):
		var body struct{ Amount money.Money }
		err = json.NewDecoder(r.Body).Decode(&body)
		if err == nil {
			err = s.platform.StubPayment(ctx, tenant, parts[len(parts)-2], body.Amount)
			result = map[string]string{"status": "paid"}
		}
	case strings.HasSuffix(r.URL.Path, "/orders"):
		s.mu.Lock()
		blocked := s.redispatch && tenant == "t1" && s.dispatchReview == nil
		s.mu.Unlock()
		if blocked {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(
				map[string]any{
					"code": checkout.DispatchOriginChanged,
					"data": map[string]any{"dispatchReviewRevision": 3},
				},
			)
			return
		}
		var body ports.CreateOrderRequest
		err = json.NewDecoder(r.Body).Decode(&body)
		if err == nil {
			var order checkout.SellerOrder
			order, err = s.platform.CreateOrder(ctx, tenant, body)
			if err == nil {
				s.mu.Lock()
				if s.tariffs == nil {
					s.tariffs = make(map[string]money.Money)
				}
				if _, saved := s.tariffs[order.OrderID]; !saved {
					s.tariffs[order.OrderID] = s.quote.Cost
				}
				s.mu.Unlock()
			}
			result = map[string]any{
				"orderId":         order.OrderID,
				"carrier":         order.Carrier,
				"pickupPointCode": order.PickupPointCode,
				"expiresAt":       order.ExpiresAt,
				"total":           order.Total,
			}
		}
	default:
		err = fmt.Errorf("неожиданный запрос: %s", r.URL)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func TestPickupCheckoutThroughShopHTTP(t *testing.T) {
	for _, action := range []string{"pay", "cancel", "redispatch"} {
		t.Run(
			action,
			func(t *testing.T) {
				ctx := context.Background()
				now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
				buyer := ids.UUID("00000000-0000-4000-8000-000000000002")
				store, backend := fake.NewStore(), fake.NewPlatform()
				backend.OrderNow = now
				upstream := &pickupCheckoutServer{platform: backend, redispatch: action == "redispatch"}
				readPickupFixture(t, "delivery_quote_available.json", &upstream.quote)
				readPickupFixture(t, "pickups.json", &upstream.points)
				server := httptest.NewServer(upstream)
				defer server.Close()
				clock := fake.NewClock(now)
				client := New(
					Config{
						BaseURL: server.URL,
						Token:   "service-token",
						Timeout: time.Second,
					},
				)
				guarded := platformguard.New(
					client,
					pickupGuardConfig(),
					clock,
					&fake.PlatformMetrics{},
					slog.Default(),
				)
				directory := appdelivery.NewService(
					store,
					guarded,
					fake.NewPickupCache(),
					appdelivery.Config{
						Attempts:     1,
						TTL:          time.Hour,
						QuoteTimeout: time.Second,
					},
				)
				index := fake.NewIndex()
				basket := cart.New("00000000-0000-4000-8000-000000000003", buyer, now, time.Hour)
				carriers := map[ids.TenantID]string{"t1": "cdek", "t2": "cdek"}
				for tenant := range carriers {
					store.SeedReadyOffer(ids.OfferID{Tenant: tenant, Product: "p1"}, money.FromKopecks(199000))
					store.SeedTenant(
						participant.TenantState{
							TenantID: tenant,
							Status:   participant.TenantActive,
						},
					)
					store.SeedParticipant(
						participant.State{
							TenantID: tenant,
							Enabled:  true,
							Stage:    participant.StageConnected,
							Delivery: participant.DeliverySettings{
								Carriers: []string{"cdek", "yandex"},
								Regions:  []string{"RU-AD"},
							},
						},
					)
					offer := cart.Offer{
						ID: ids.OfferID{
							Tenant:  tenant,
							Product: "p1",
						},
						Name:      "Товар",
						Price:     money.FromKopecks(199000),
						Available: 3,
						Visible:   true,
					}
					if err := basket.Add(offer, 1, now, time.Hour); err != nil {
						t.Fatal(err)
					}
					if err := index.Put(
						ctx,
						[]ports.OfferDocument{
							{
								ID:                offer.ID.String(),
								TenantID:          string(tenant),
								ProductID:         "p1",
								Visible:           true,
								CheckoutAvailable: true,
								Version:           1,
							},
						},
					); err != nil {
						t.Fatal(err)
					}
				}
				if err := store.Do(
					ctx,
					func(ctx context.Context, tx ports.Tx) error {
						return tx.Carts().Save(ctx, basket)
					},
				); err != nil {
					t.Fatal(err)
				}
				service := appcheckout.NewService(
					store,
					guarded,
					directory,
					&fake.CartVerifier{},
					&fake.IDs{},
					clock,
					appcheckout.Config{
						QuoteTTL:     time.Minute,
						QuoteTimeout: time.Second,
						PaymentStub:  true,
					},
				)
				state, err := service.Create(ctx, buyer, "pickup-flow-0001")
				if err != nil {
					t.Fatal(err)
				}
				for _, carrier := range []string{"cdek"} {
					page, err := directory.QueryFor(
						ctx,
						delivery.Carrier(carrier),
						"Город & район",
						[]string{"RU-AD"},
						nil,
						0,
						200,
					)
					points := page.Items
					if err != nil || len(points) != 1 {
						t.Fatal(points, err)
					}
					state, err = service.Quote(
						ctx,
						buyer,
						state.ID,
						delivery.Carrier(carrier),
						"p1",
						"Город & район",
					)
					if err != nil {
						t.Fatal(err)
					}
				}
				state, err = service.Confirm(ctx, buyer, state.ID)
				if err != nil || state.Total != state.GoodsTotal || state.DeliveryTotal.Kopecks() != 0 {
					t.Fatal(state, err)
				}
				for _, group := range state.Groups {
					if group.Quote.Cost != upstream.quote.Cost ||
						state.Delivery.Carrier != delivery.Carrier(carriers[group.TenantID]) {
						t.Fatal(group)
					}
				}
				contact := checkout.Contact{
					Phone: "79251234567",
					Name:  "Покупатель",
				}
				if _, err := service.CreateOrders(ctx, buyer, state.ID, contact); err != nil {
					t.Fatal(err)
				}
				runPickupTasks(t, store, service, now)
				if action == "redispatch" {
					review, err := service.Get(ctx, buyer, state.ID)
					if err != nil || review.Status != checkout.StatusDraft || backend.OrderCount() != 1 {
						t.Fatal(
							"нужно новое подтверждение без потери здорового заказа",
							review,
							err,
						)
					}
					for _, group := range review.Groups {
						if group.TenantID == "t1" && (group.Status != checkout.GroupDeliveryRequoted ||
							group.Reason != checkout.DispatchOriginChanged || group.Quote.MinDays != 7 ||
							group.Quote.MaxDays != 9 || !group.DeliveryPrice.IsZero() || group.Excluded()) {
							t.Fatal(group)
						}
					}
					upstream.mu.Lock()
					recalculation := upstream.dispatchReview
					upstream.mu.Unlock()
					if recalculation == nil || recalculation.Revision != 3 || recalculation.GroupID != "t1" {
						t.Fatal(
							"HTTP-пересчёт не подтвердил сохранённое поколение отказа",
							recalculation,
						)
					}
					if _, err := service.Confirm(ctx, buyer, state.ID); err != nil {
						t.Fatal(err)
					}
					if _, err := service.CreateOrders(ctx, buyer, state.ID, contact); err != nil {
						t.Fatal(err)
					}
					runPickupTasks(t, store, service, now)
				}
				if _, err := service.CreateOrders(
					ctx,
					buyer,
					state.ID,
					contact,
				); err != nil ||
					backend.OrderCount() != 2 {
					t.Fatal("повтор изменил число заказов", err, backend.OrderCount())
				}
				for tenant, carrier := range carriers {
					requests := backend.OrderRequests(tenant)
					if len(requests) != 1 || requests[0].Carrier != carrier ||
						requests[0].DeliveryPrice.Kopecks() != 0 {
						t.Fatal(requests)
					}
					if _, err := guarded.CreateOrder(ctx, tenant, requests[0]); err != nil ||
						backend.OrderCount() != 2 {
						t.Fatal("повтор HTTP изменил заказ", err)
					}
				}
				state, err = service.Get(ctx, buyer, state.ID)
				if err != nil {
					t.Fatal(err)
				}
				upstream.mu.Lock()
				for _, group := range state.Groups {
					if upstream.tariffs[group.OrderID] != upstream.quote.Cost || group.DeliveryPrice.Kopecks() != 0 {
						t.Error(
							"расход продавца потерян или попал в сумму покупателя",
							group,
						)
					}
				}
				upstream.mu.Unlock()
				if action != "cancel" {
					if err := service.ConfirmPayment(ctx, buyer, state.ID); err != nil {
						t.Fatal(err)
					}
					if err := service.ConfirmPayment(ctx, buyer, state.ID); err != nil {
						t.Fatal(err)
					}
					state, err = service.Get(ctx, buyer, state.ID)
					if err != nil || state.Status != checkout.StatusPaid {
						t.Fatal(state, err)
					}
				} else {
					if _, err := service.Cancel(ctx, buyer, state.ID); err != nil {
						t.Fatal(err)
					}
					runPickupTasks(t, store, service, now)
					state, err = service.Get(ctx, buyer, state.ID)
					if err != nil || state.Status != checkout.StatusCancelled {
						t.Fatal(state, err)
					}
					for _, group := range state.Groups {
						if group.Status != checkout.GroupCancelled {
							t.Fatal(group)
						}
					}
				}
			},
		)
	}
}

func readPickupFixture(t *testing.T, name string, target any) {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}

func runPickupTasks(t *testing.T, store *fake.Store, service *appcheckout.Service, now time.Time) {
	t.Helper()
	tasks, err := store.Claim(
		context.Background(),
		ports.ClaimRequest{
			Now:        now,
			Limit:      200,
			PerTenant:  200,
			LeaseUntil: now.Add(time.Second),
			Kinds:      []string{appcheckout.TaskCreateOrder, appcheckout.TaskCancelOrder},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Сначала здоровая группа: её заказ должен пережить пересчёт другой группы.
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TenantID > tasks[j].TenantID })
	for _, task := range tasks {
		if err := service.HandleOrderTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		if err := store.Complete(context.Background(), task.ID, task.Attempts, now); err != nil {
			t.Fatal(err)
		}
	}
}

func pickupGuardConfig() platformguard.Config {
	return platformguard.Config{
		ReadTimeout:           time.Second,
		StrictTimeout:         time.Second,
		CacheTTL:              time.Minute,
		CacheSize:             10,
		MaxConcurrent:         4,
		CheckoutMaxConcurrent: 2,
		ReadMaxConcurrent:     2,
		TenantMaxConcurrent:   2,
		QueueSize:             8,
		TenantQueueSize:       2,
		TenantStateSize:       100,
		TenantIdleTTL:         time.Minute,
		FailureThreshold:      5,
		FailureWindow:         time.Second,
		FailureMinCalls:       20,
		FailurePercent:        50,
		OpenInterval:          time.Second,
		LogInterval:           time.Minute,
	}
}
