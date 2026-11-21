package http

import "net/http"

type variantGroupJSON struct {
	GroupID string `json:"groupId"`
}

func (h *catalogHandlers) variants(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=30")
	block, err := h.queries.Variants(
		r.Context(),
		r.PathValue("tenantId"),
		r.PathValue("groupId"),
	)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, variantResponse(block))
}
