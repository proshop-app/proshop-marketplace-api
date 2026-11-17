package http

import (
	"net/http"
	"strconv"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type revalidationCursor struct {
	TenantID  ids.TenantID  `json:"tenantId"`
	ProductID ids.ProductID `json:"productId"`
}

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
	var next *revalidationCursor
	if len(failures) == limit {
		last := failures[len(failures)-1]
		next = &revalidationCursor{TenantID: last.TenantID, ProductID: last.ProductID}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"items": failures,
		"next":  next,
	})
}
