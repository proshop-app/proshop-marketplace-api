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
	)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", publicCatalogCacheControl)
	writeJSON(w, http.StatusOK, variantResponse(block))
}
