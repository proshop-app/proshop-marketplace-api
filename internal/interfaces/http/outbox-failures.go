package http

import (
	"net/http"
	"strconv"
)

func (h *operatorHandlers) outboxFailures(w http.ResponseWriter, r *http.Request) {
	after, limit := int64(0), 50
	if raw := r.URL.Query().Get("after"); raw != "" {
		var err error
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, r, ErrInvalidQuery)
			return
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeError(w, r, ErrInvalidQuery)
			return
		}
	}
	items, err := h.dispatcher.Failures(r.Context(), after, limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
