package http

import (
	"net/http"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
)

func (h *catalogHandlers) populateSellers(r *http.Request, cards []appcatalog.OfferCard) bool {
	err := appcatalog.PopulateSellers(r.Context(), h.sellers, cards)
	if r.Context().Err() != nil {
		return false
	}
	if err != nil {
		loggerFrom(r.Context()).Warn("каталог: подсчёт магазинов недоступен", "error", err)
	}
	return true
}
