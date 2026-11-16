package http

import "net/http"

type variantGroupJSON struct {
	GroupID string `json:"groupId"`
}

func (h *catalogHandlers) variants(w http.ResponseWriter, r *http.Request) {
	block, err := h.queries.Variants(
		r.Context(),
		r.PathValue("tenantId"),
		r.PathValue("groupId"),
		BuyerID(r.Context()),
	)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, variantResponse(block))
}
