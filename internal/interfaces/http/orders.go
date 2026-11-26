package http

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type createOrdersJSON struct {
	Contact checkout.Contact `json:"contact"`
}

type checkoutListJSON struct {
	Items  []json.RawMessage       `json:"items"`
	Next   string                  `json:"next"`
	Issues []appcheckout.ReadIssue `json:"issues,omitempty"`
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

// paymentReturn игнорирует параметры браузера: статус подтверждается сервером через оплату или опрос.
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
	states := make([]checkout.State, 0, len(purchases.Items))
	for _, purchase := range purchases.Items {
		states = append(states, purchase.State())
	}
	page := appcheckout.ListPage[checkout.State]{
		ReadDiagnostics: purchases.ReadDiagnostics,
		Items:           states,
		Next:            purchases.Next,
	}
	h.respondList(w, r, page, false)
}

// pickupHistory — пункты выдачи прошлых заказов для подсказки на оформлении, без снимков покупок.
func (h *checkoutHandlers) pickupHistory(w http.ResponseWriter, r *http.Request) {
	points, err := h.checkouts.PickupHistory(r.Context(), BuyerID(r.Context()))
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]selectionJSON, 0, len(points))
	for _, point := range points {
		items = append(items, toSelectionJSON(point))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *checkoutHandlers) operatorList(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("stuck")
	if filter != "" && filter != "true" && filter != "false" {
		writeError(w, r, ErrInvalidQuery)
		return
	}
	after, err := operatorOrderCursor(r, filter == "true")
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.checkouts.OperatorList(r.Context(), after, filter == "true")
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.respondList(w, r, page, true)
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

func operatorOrderCursor(r *http.Request, stuck bool) (checkout.ListCursor, error) {
	raw := r.URL.Query().Get("after")
	if !stuck || raw == "" {
		id, err := orderCursor(r)
		return checkout.ListCursor{ID: id}, err
	}

	date, rawID, found := strings.Cut(raw, "/")
	at, timeErr := time.Parse(time.RFC3339Nano, date)
	id, idErr := ids.ParseUUID(rawID)
	if !found || timeErr != nil || idErr != nil || !checkout.ValidStuckCursor(&at) {
		return checkout.ListCursor{}, ErrInvalidQuery
	}
	return checkout.ListCursor{ID: id, StuckAt: &at}, nil
}

func (h *checkoutHandlers) respondList(
	w http.ResponseWriter,
	r *http.Request,
	page appcheckout.ListPage[checkout.State],
	operator bool,
) {
	sellers, err := h.checkouts.Sellers(r.Context(), page.Items)
	if err != nil {
		writeError(w, r, err)
		return
	}

	response := checkoutListJSON{Items: make([]json.RawMessage, 0, len(page.Items)), Next: listCursorText(page.Next)}
	for _, state := range page.Items {
		// Покупка сериализуется один раз: ошибка представления одной не лишает оператора остальных.
		item, err := renderListItem(state, sellers.Items)
		if err != nil {
			issues := page.ReportReadRenderingFailure(r.Context(), state, err)
			if operator {
				page.Issues = append(page.Issues, issues...)
			}
			continue
		}
		response.Items = append(response.Items, item)
	}
	logSellerReadFailures(r.Context(), sellers.Failures, &page.ReadDiagnostics)
	if operator {
		response.Issues = page.Issues
	}
	writeJSON(w, http.StatusOK, response)
}

func renderListItem(state checkout.State, sellers map[ids.TenantID]*participant.Seller) (json.RawMessage, error) {
	item, err := toCheckoutJSON(state)
	if err != nil {
		return nil, err
	}
	setGroupSellers(&item, sellers)
	return json.Marshal(item)
}

// listCursorText — ключ продолжения: у «зависших» он несёт и время попадания в список.
func listCursorText(cursor checkout.ListCursor) string {
	if cursor.StuckAt == nil {
		return string(cursor.ID)
	}
	return cursor.StuckAt.UTC().Format(time.RFC3339Nano) + "/" + string(cursor.ID)
}

func (h *checkoutHandlers) cancel(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)
		return
	}
	state, err := h.checkouts.Cancel(r.Context(), BuyerID(r.Context()), id)
	h.respond(w, r, http.StatusOK, state, err)
}

func (h *checkoutHandlers) removeGroup(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)
		return
	}
	state, err := h.checkouts.RemoveGroup(r.Context(), BuyerID(r.Context()), id, ids.TenantID(r.PathValue("tenantId")))
	h.respond(w, r, http.StatusOK, state, err)
}
