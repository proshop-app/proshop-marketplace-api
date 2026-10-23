package http

import "net/http"

type deliverySettingsJSON struct {
	DeliverySubsidyMinPercent int `json:"deliverySubsidyMinPercent"`
}

// deliverySettings — публичное правило площадки для админки магазина и витрины.
func (h *checkoutHandlers) deliverySettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, deliverySettingsJSON{
		DeliverySubsidyMinPercent: h.checkouts.DeliverySubsidyMinPercent(),
	})
}
