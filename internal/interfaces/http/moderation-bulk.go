package http

import (
	"net/http"

	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (h *moderationHandlers) bulkAllow(w http.ResponseWriter, r *http.Request) {
	var body appmoderation.BulkCommand
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	principal := principalFrom(r)
	body.Operator = moderation.Actor{ID: principal.ID, Name: principal.Name}
	body.IdempotencyKey = r.Header.Get("Idempotency-Key")
	result, err := h.service.BulkAllow(r.Context(), body)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *moderationHandlers) bulkCount(w http.ResponseWriter, r *http.Request) {
	selector := moderation.BulkSelector{
		TenantID: ids.TenantID(r.URL.Query().Get("tenantId")),
		Reasons:  r.URL.Query()["reason"],
	}
	count, err := h.service.BulkCount(r.Context(), selector)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"total": count})
}
