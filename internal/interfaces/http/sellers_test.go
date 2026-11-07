package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type sellerCountingUOW struct {
	ports.UnitOfWork
	calls   int
	tenants int
}

type sellerCountingTx struct {
	ports.Tx
	owner *sellerCountingUOW
}

type sellerCountingRepository struct {
	participant.Repository
	owner *sellerCountingUOW
}

func (u *sellerCountingUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return u.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return fn(ctx, sellerCountingTx{Tx: tx, owner: u})
	})
}

func (tx sellerCountingTx) Participants() participant.Repository {
	return sellerCountingRepository{Repository: tx.Tx.Participants(), owner: tx.owner}
}

func (r sellerCountingRepository) Sellers(
	ctx context.Context,
	tenants []ids.TenantID,
) (map[ids.TenantID]*participant.Seller, error) {
	r.owner.calls++
	r.owner.tenants = len(tenants)
	return r.Repository.Sellers(ctx, tenants)
}

func TestSellerResponsesBatchAndNull(t *testing.T) {
	store := fake.NewStore()
	store.SeedParticipant(participant.State{TenantID: "t1", Slug: "shop-one", Name: "Магазин"})
	store.SeedParticipant(participant.State{TenantID: "t2", Slug: "shop-two", Name: "Другой магазин"})
	uow := &sellerCountingUOW{UnitOfWork: store}
	clock := fake.NewClock(time.Now())
	carts := appcart.NewService(uow, fake.NewIndex(), fake.NewPlatform(), &fake.IDs{}, clock, time.Hour)
	checkouts := appcheckout.NewService(
		uow,
		fake.NewIndex(),
		fake.NewPlatform(),
		nil,
		carts,
		&fake.IDs{},
		clock,
		appcheckout.Config{},
	)
	state := cart.State{Lines: []cart.Line{
		{Offer: ids.OfferID{Tenant: "t1", Product: "p1"}, Quantity: 1},
		{Offer: ids.OfferID{Tenant: "t1", Product: "p2"}, Quantity: 1},
		{Offer: ids.OfferID{Tenant: "missing", Product: "p1"}, Quantity: 1},
	}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/cart", nil)
	response := httptest.NewRecorder()
	(&cartHandlers{carts: carts}).respond(response, request, state, nil)
	var body struct {
		Lines []map[string]json.RawMessage `json:"lines"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(body.Lines) != 3 {
		t.Fatal(response.Body.String())
	}
	assertSellerJSON(t, body.Lines[0]["seller"], "shop-one", "Магазин")
	if string(body.Lines[2]["seller"]) != "null" || uow.calls != 1 || uow.tenants != 2 {
		t.Fatal(response.Body.String(), uow.calls, uow.tenants)
	}
	states := []checkout.State{
		{Groups: []checkout.Group{
			{TenantID: "t1", Tracking: &checkout.Tracking{Carrier: "carrier", URL: "https://example.test/track"}},
			{TenantID: "missing"},
		}},
		{Groups: []checkout.Group{{TenantID: "t1"}, {TenantID: "t2"}}},
	}
	h := &checkoutHandlers{checkouts: checkouts}
	for _, mode := range []string{"checkout", "orders", "delivery_changed"} {
		t.Run(mode, func(t *testing.T) {
			uow.calls = 0
			response = httptest.NewRecorder()
			switch mode {
			case "orders":
				h.respondList(response, request, states)
			case "delivery_changed":
				h.respond(response, request, http.StatusOK, states[0], checkout.ErrDeliveryChanged)
			default:
				h.respond(response, request, http.StatusOK, states[0], nil)
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			raw := response.Body.Bytes()
			if mode == "orders" {
				var items []json.RawMessage
				if err := json.Unmarshal(result["items"], &items); err != nil {
					t.Fatal(err)
				}
				if len(items) != 2 || uow.tenants != 3 {
					t.Fatal(response.Body.String(), uow.tenants)
				}
				raw = items[0]
			}
			if mode == "delivery_changed" {
				raw = result["checkout"]
			}
			var purchase struct {
				Groups []map[string]json.RawMessage `json:"groups"`
			}
			if err := json.Unmarshal(raw, &purchase); err != nil {
				t.Fatal(err)
			}
			if len(purchase.Groups) != 2 {
				t.Fatal(response.Body.String())
			}
			assertSellerJSON(t, purchase.Groups[0]["seller"], "shop-one", "Магазин")
			var tracking checkout.Tracking
			if err := json.Unmarshal(purchase.Groups[0]["tracking"], &tracking); err != nil {
				t.Fatal(err)
			}
			if tracking.Carrier != "carrier" || tracking.URL != "https://example.test/track" {
				t.Fatal(tracking)
			}
			if string(purchase.Groups[1]["seller"]) != "null" || uow.calls != 1 {
				t.Fatal(response.Body.String(), uow.calls)
			}
		})
	}
}

func assertSellerJSON(t *testing.T, raw json.RawMessage, slug, name string) {
	t.Helper()
	var seller map[string]string
	if err := json.Unmarshal(raw, &seller); err != nil {
		t.Fatal(err)
	}
	if len(seller) != 2 || seller["slug"] != slug || seller["name"] != name {
		t.Fatal(string(raw))
	}
}
