package http

import (
	"net/http"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type checkoutLineJSON struct {
	OfferID   string `json:"offerId"`
	ProductID string `json:"productId"`
	Name      string `json:"name"`
	Quantity  int    `json:"quantity"`
	Price     int64  `json:"price"`
	Amount    int64  `json:"amount"`
}

type quoteJSON struct {
	Status    string    `json:"status"`
	Reason    string    `json:"reason,omitempty"`
	Cost      int64     `json:"cost"`
	Subsidy   int64     `json:"subsidy"`
	BuyerCost int64     `json:"buyerCost"`
	MinDays   int       `json:"minDays"`
	MaxDays   int       `json:"maxDays"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type checkoutGroupJSON struct {
	TenantID           string             `json:"tenantId"`
	ExternalCheckoutID string             `json:"externalCheckoutId"`
	Lines              []checkoutLineJSON `json:"lines"`
	Goods              int64              `json:"goods"`
	Quote              *quoteJSON         `json:"quote"`
}

type selectionJSON struct {
	Carrier         string `json:"carrier"`
	PickupPointCode string `json:"pickupPointCode"`
	City            string `json:"city"`
	Address         string `json:"address"`
}

// checkoutJSON — покупка; суммы — копейки.
type checkoutJSON struct {
	ID            string              `json:"id"`
	Status        string              `json:"status"`
	Groups        []checkoutGroupJSON `json:"groups"`
	Delivery      *selectionJSON      `json:"delivery"`
	GoodsTotal    int64               `json:"goodsTotal"`
	DeliveryTotal int64               `json:"deliveryTotal"`
	Total         int64               `json:"total"`
	Version       int64               `json:"version"`
	CreatedAt     time.Time           `json:"createdAt"`
	UpdatedAt     time.Time           `json:"updatedAt"`
	ConfirmedAt   *time.Time          `json:"confirmedAt"`
}

type checkoutHandlers struct {
	checkouts *appcheckout.Service
}

type quoteRequestJSON struct {
	Carrier         string `json:"carrier"`
	PickupPointCode string `json:"pickupPointCode"`
}

func toCheckoutJSON(state checkout.State) (checkoutJSON, error) {
	out := checkoutJSON{
		ID:            string(state.ID),
		Status:        string(state.Status),
		Groups:        make([]checkoutGroupJSON, 0, len(state.Groups)),
		GoodsTotal:    state.GoodsTotal.Kopecks(),
		DeliveryTotal: state.DeliveryTotal.Kopecks(),
		Total:         state.Total.Kopecks(),
		Version:       state.Version,
		CreatedAt:     state.CreatedAt.UTC(),
		UpdatedAt:     state.UpdatedAt.UTC(),
	}

	if !state.ConfirmedAt.IsZero() {
		confirmed := state.ConfirmedAt.UTC()
		out.ConfirmedAt = &confirmed
	}

	if d := state.Delivery; d != nil {
		out.Delivery = &selectionJSON{
			Carrier:         string(d.Carrier),
			PickupPointCode: d.PickupPointCode,
			City:            d.City,
			Address:         d.Address,
		}
	}

	for _, group := range state.Groups {
		g := checkoutGroupJSON{
			TenantID:           string(group.TenantID),
			ExternalCheckoutID: group.ExternalCheckoutID,
			Lines:              make([]checkoutLineJSON, 0, len(group.Lines)),
			Goods:              group.Goods.Kopecks(),
		}

		for _, line := range group.Lines {
			amount, err := line.Price.MulRatio(int64(line.Quantity), 1)
			if err != nil {
				return checkoutJSON{}, err
			}

			g.Lines = append(g.Lines, checkoutLineJSON{
				OfferID:   line.Offer.String(),
				ProductID: string(line.Offer.Product),
				Name:      line.Name,
				Quantity:  line.Quantity,
				Price:     line.Price.Kopecks(),
				Amount:    amount.Kopecks(),
			})
		}

		if q := group.Quote; q != nil {
			g.Quote = &quoteJSON{
				Status:    string(q.Status),
				Reason:    q.Reason,
				Cost:      q.Cost.Kopecks(),
				Subsidy:   q.Subsidy.Kopecks(),
				BuyerCost: q.BuyerCost.Kopecks(),
				MinDays:   q.MinDays,
				MaxDays:   q.MaxDays,
				ExpiresAt: q.ExpiresAt.UTC(),
			}
		}

		out.Groups = append(out.Groups, g)
	}

	return out, nil
}

func (h *checkoutHandlers) respond(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	state checkout.State,
	err error,
) {
	if err != nil {
		writeError(w, r, err)

		return
	}

	body, err := toCheckoutJSON(state)
	if err != nil {
		writeError(w, r, err)

		return
	}

	writeJSON(w, status, body)
}

// create — покупка из корзины; заголовок Idempotency-Key обязателен.
func (h *checkoutHandlers) create(w http.ResponseWriter, r *http.Request) {
	state, err := h.checkouts.Create(r.Context(), BuyerID(r.Context()), r.Header.Get("Idempotency-Key"))
	h.respond(w, r, http.StatusCreated, state, err)
}

func (h *checkoutHandlers) get(w http.ResponseWriter, r *http.Request) {
	checkoutID, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)

		return
	}

	state, err := h.checkouts.Get(r.Context(), BuyerID(r.Context()), checkoutID)
	h.respond(w, r, http.StatusOK, state, err)
}

// quote — выбрать пункт выдачи и посчитать доставку у продавцов.
func (h *checkoutHandlers) quote(w http.ResponseWriter, r *http.Request) {
	checkoutID, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)

		return
	}

	var body quoteRequestJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)

		return
	}

	carrier, err := delivery.ParseCarrier(body.Carrier)
	if err != nil {
		writeError(w, r, err)

		return
	}

	state, err := h.checkouts.Quote(r.Context(), BuyerID(r.Context()), checkoutID, carrier, body.PickupPointCode)
	h.respond(w, r, http.StatusOK, state, err)
}

// confirm — перепроверить у магазинов и подтвердить покупку.
func (h *checkoutHandlers) confirm(w http.ResponseWriter, r *http.Request) {
	checkoutID, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)

		return
	}

	state, err := h.checkouts.Confirm(r.Context(), BuyerID(r.Context()), checkoutID)
	h.respond(w, r, http.StatusOK, state, err)
}
