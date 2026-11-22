package http

import (
	"net/http"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type returnRequestJSON struct {
	Reason checkout.ReturnReason `json:"reason"`
}

func (h *checkoutHandlers) cancelGroupOrder(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)
		return
	}
	tenant := ids.TenantID(r.PathValue("tenantId"))
	state, err := h.checkouts.CancelPaidOrder(r.Context(), BuyerID(r.Context()), id, tenant)
	h.respond(w, r, http.StatusOK, state, err)
}

func (h *checkoutHandlers) requestReturn(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)
		return
	}
	var body returnRequestJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	tenant := ids.TenantID(r.PathValue("tenantId"))
	state, err := h.checkouts.RequestReturn(r.Context(), BuyerID(r.Context()), id, tenant, body.Reason)
	h.respond(w, r, http.StatusOK, state, err)
}
