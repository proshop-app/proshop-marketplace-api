package http

import (
	"net/http"
	"time"

	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type cartLineJSON struct {
	Seller    *participant.Seller `json:"seller"`
	OfferID   string              `json:"offerId"`
	TenantID  string              `json:"tenantId"`
	ProductID string              `json:"productId"`
	Name      string              `json:"name"`
	Quantity  int                 `json:"quantity"`
	Price     int64               `json:"price"`
	Amount    int64               `json:"amount"`
	Issue     *string             `json:"issue"`
	AddedAt   time.Time           `json:"addedAt"`
}

// cartJSON — корзина; суммы — копейки.
type cartJSON struct {
	Groups    []cartGroupJSON `json:"groups"`
	ID        string          `json:"id"`
	Lines     []cartLineJSON  `json:"lines"`
	Total     int64           `json:"total"`
	Version   int64           `json:"version"`
	ExpiresAt time.Time       `json:"expiresAt"`
}

type cartHandlers struct {
	carts *appcart.Service
}

type addLineJSON struct {
	OfferID  string `json:"offerId"`
	Quantity int    `json:"quantity"`
}

type quantityJSON struct {
	Quantity int `json:"quantity"`
}

func toCartJSON(state cart.State) (cartJSON, error) {
	out := cartJSON{
		ID:        string(state.ID),
		Groups:    make([]cartGroupJSON, 0),
		Lines:     make([]cartLineJSON, 0, len(state.Lines)),
		Version:   state.Version,
		ExpiresAt: state.ExpiresAt.UTC(),
	}

	total, err := cart.Restore(state).Total()
	if err != nil {
		return cartJSON{}, err
	}

	out.Total = total.Kopecks()

	for _, line := range state.Lines {
		amount, err := cart.LineAmount(line)
		if err != nil {
			return cartJSON{}, err
		}

		var issue *string
		if line.Issue != cart.IssueNone {
			value := string(line.Issue)
			issue = &value
		}

		out.Lines = append(out.Lines, cartLineJSON{
			OfferID:   line.Offer.String(),
			TenantID:  string(line.Offer.Tenant),
			ProductID: string(line.Offer.Product),
			Name:      line.Name,
			Quantity:  line.Quantity,
			Price:     line.Price.Kopecks(),
			Amount:    amount.Kopecks(),
			Issue:     issue,
			AddedAt:   line.AddedAt.UTC(),
		})
	}

	for _, group := range cart.Restore(state).Groups() {
		g := cartGroupJSON{TenantID: string(group.TenantID), PromoCode: cartPromoResponse(group.PromoCode)}
		total := money.Money{}
		for _, line := range group.Lines {
			amount, err := cart.LineAmount(line)
			if err != nil {
				return cartJSON{}, err
			}
			total, err = total.Add(amount)
			if err != nil {
				return cartJSON{}, err
			}
		}
		g.Total = total.Kopecks()
		out.Groups = append(out.Groups, g)
	}
	return out, nil
}

func (h *cartHandlers) respond(w http.ResponseWriter, r *http.Request, state cart.State, err error) {
	if err != nil {
		writeError(w, r, err)

		return
	}

	body, err := toCartJSON(state)
	if err != nil {
		writeError(w, r, err)

		return
	}

	sellers, err := h.carts.Sellers(r.Context(), state)
	if err != nil {
		writeError(w, r, err)
		return
	}
	for i := range body.Lines {
		body.Lines[i].Seller = sellers[ids.TenantID(body.Lines[i].TenantID)]
	}
	writeJSON(w, http.StatusOK, body)
}

func (h *cartHandlers) get(w http.ResponseWriter, r *http.Request) {
	state, err := h.carts.Verify(r.Context(), BuyerID(r.Context()))
	h.respond(w, r, state, err)
}

func (h *cartHandlers) add(w http.ResponseWriter, r *http.Request) {
	var body addLineJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)

		return
	}

	offerID, err := ids.ParseOfferID(body.OfferID)
	if err != nil {
		writeError(w, r, err)

		return
	}

	state, err := h.carts.Add(r.Context(), BuyerID(r.Context()), offerID, body.Quantity)
	h.respond(w, r, state, err)
}

func (h *cartHandlers) setQuantity(w http.ResponseWriter, r *http.Request) {
	var body quantityJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)

		return
	}

	offerID, err := ids.ParseOfferID(r.PathValue("offerId"))
	if err != nil {
		writeError(w, r, err)

		return
	}

	state, err := h.carts.SetQuantity(r.Context(), BuyerID(r.Context()), offerID, body.Quantity)
	h.respond(w, r, state, err)
}

func (h *cartHandlers) remove(w http.ResponseWriter, r *http.Request) {
	offerID, err := ids.ParseOfferID(r.PathValue("offerId"))
	if err != nil {
		writeError(w, r, err)

		return
	}

	state, err := h.carts.Remove(r.Context(), BuyerID(r.Context()), offerID)
	h.respond(w, r, state, err)
}

func (h *cartHandlers) verify(w http.ResponseWriter, r *http.Request) {
	state, err := h.carts.Verify(r.Context(), BuyerID(r.Context()))
	h.respond(w, r, state, err)
}
