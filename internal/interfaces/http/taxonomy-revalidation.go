package http

import (
	"net/http"
	"strconv"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (h *taxonomyHandlers) revalidationFailures(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil {
			writeError(w, r, taxonomy.ErrInvalid)
			return
		}
	}
	after := ports.OfferKey{
		TenantID:  ids.TenantID(r.URL.Query().Get("afterTenant")),
		ProductID: ids.ProductID(r.URL.Query().Get("afterProduct")),
	}
	failures, err := h.service.RevalidationFailures(r.Context(), after, limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "unverified",
		"items":  failures,
	})
}
