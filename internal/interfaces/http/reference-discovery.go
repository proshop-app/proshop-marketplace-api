package http

import "net/http"

func (h *referenceHandlers) discovery(w http.ResponseWriter, r *http.Request) {
	dictionaries, err := h.service.Dictionaries(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, discoveryJSON{SchemaVersion: 1, Dictionaries: dictionaries})
}
