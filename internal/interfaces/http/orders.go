package http

import (
	"net/http"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type createOrdersJSON struct {
	Contact checkout.Contact `json:"contact"`
}

func (h *checkoutHandlers) createOrders(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)
		return
	}
	var body createOrdersJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	state, err := h.checkouts.CreateOrders(r.Context(), BuyerID(r.Context()), id, body.Contact)
	h.respond(w, r, http.StatusOK, state, err)
}

func (h *checkoutHandlers) pay(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)
		return
	}
	tenant, err := ids.ParseTenantID(r.PathValue("groupId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)
		return
	}
	link, err := h.checkouts.Pay(r.Context(), BuyerID(r.Context()), id, tenant)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, link)
}

// paymentReturn игнорирует любые параметры кассы. Только опрос магазина меняет статус.
func (h *checkoutHandlers) paymentReturn(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)
		return
	}
	if _, err := h.checkouts.Get(r.Context(), BuyerID(r.Context()), id); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "checking_payment",
		"message":    "Проверяем оплату",
		"checkoutId": string(id),
	})
}

func (h *checkoutHandlers) orders(w http.ResponseWriter, r *http.Request) {
	after, err := orderCursor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	purchases, err := h.checkouts.Purchases(r.Context(), BuyerID(r.Context()), after)
	if err != nil {
		writeError(w, r, err)
		return
	}
	states := make([]checkout.State, 0, len(purchases))
	for _, purchase := range purchases {
		states = append(states, purchase.State())
	}
	h.respondList(w, r, states)
}

func (h *checkoutHandlers) operatorList(w http.ResponseWriter, r *http.Request) {
	after, err := orderCursor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	filter := r.URL.Query().Get("stuck")
	if filter != "" && filter != "true" && filter != "false" {
		writeError(w, r, ErrInvalidQuery)
		return
	}
	states, err := h.checkouts.OperatorList(r.Context(), after, filter == "true")
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.respondList(w, r, states)
}

func orderCursor(r *http.Request) (ids.UUID, error) {
	raw := r.URL.Query().Get("after")
	if raw == "" {
		return "", nil
	}
	id, err := ids.ParseUUID(raw)
	if err != nil {
		return "", ErrInvalidQuery
	}
	return id, nil
}

func (h *checkoutHandlers) respondList(w http.ResponseWriter, r *http.Request, states []checkout.State) {
	sellers, err := h.checkouts.Sellers(r.Context(), states)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]checkoutJSON, 0, len(states))
	next := ""
	for _, state := range states {
		item, err := toCheckoutJSON(state, h.checkouts.NextPayment(state))
		if err != nil {
			writeError(w, r, err)
			return
		}
		setGroupSellers(&item, sellers)
		items = append(items, item)
		next = string(state.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next": next})
}
