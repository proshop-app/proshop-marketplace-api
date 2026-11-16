package http

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type statusHandlers struct{ service *moderationstatus.Service }

func (h *statusHandlers) get(w http.ResponseWriter, r *http.Request) {
	tenant, err := ids.ParseTenantID(r.PathValue("tenantId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	products, err := statusProductIDs(r.URL.Query())
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := h.service.Get(r.Context(), tenant, products)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func statusProductIDs(query url.Values) ([]ids.ProductID, error) {
	values, supplied := query["ids"]
	if !supplied {
		return nil, nil
	}
	if len(values) != 1 {
		return nil, moderation.ErrInvalid
	}
	parts := strings.Split(values[0], ",")
	if len(parts) > moderationstatus.MaxProductIDs {
		return nil, moderation.ErrInvalid
	}
	products := make([]ids.ProductID, 0, len(parts))
	seen := make(map[ids.ProductID]bool)
	for _, raw := range parts {
		// Как в магазине: непустой неизвестный id даёт отсутствие статуса, а не ошибку запроса.
		id := ids.ProductID(strings.TrimSpace(raw))
		if id == "" {
			return nil, moderation.ErrInvalid
		}
		if !seen[id] {
			products = append(products, id)
			seen[id] = true
		}
	}
	return products, nil
}
