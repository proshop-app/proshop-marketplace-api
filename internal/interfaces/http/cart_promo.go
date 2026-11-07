package http

import (
	"net/http"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type cartPromoJSON struct {
	Code     string  `json:"code"`
	Valid    bool    `json:"valid"`
	Discount int64   `json:"discount"`
	Message  *string `json:"message"`
}

type cartGroupJSON struct {
	TenantID  string         `json:"tenantId"`
	Total     int64          `json:"total"`
	PromoCode *cartPromoJSON `json:"promoCode"`
}

type promoCodeJSON struct {
	Code string `json:"code"`
}

func (h *cartHandlers) promo(w http.ResponseWriter, r *http.Request) {
	tenant, err := ids.ParseTenantID(r.PathValue("tenantId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body promoCodeJSON
	if r.Method == http.MethodPut {
		if err := readJSON(w, r, &body); err != nil {
			writeError(w, r, err)
			return
		}
		if strings.TrimSpace(body.Code) == "" {
			writeError(w, r, cart.ErrPromoInvalid)
			return
		}
	}
	state, err := h.carts.SetPromo(r.Context(), BuyerID(r.Context()), tenant, body.Code)
	h.respond(w, r, state, err)
}

func cartPromoResponse(promo *cart.PromoCode) *cartPromoJSON {
	if promo == nil {
		return nil
	}
	return &cartPromoJSON{
		Code:     promo.Code,
		Valid:    promo.Valid,
		Discount: promo.Discount.Kopecks(),
		Message:  promo.Message,
	}
}
