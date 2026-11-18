package http

import (
	"errors"
	"math"
	"net/http"
	"strconv"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

type cityPageJSON struct {
	Items []ports.CitySuggestion `json:"items"`
}

type cityHandlers struct {
	service        *appbuyer.Cities
	trustedProxies int
}

func (h *cityHandlers) detect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	result, err := h.service.Detect(r.Context(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeCityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *cityHandlers) suggest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	values := r.URL.Query()
	if len(values["value"]) != 1 {
		writeError(w, r, ErrInvalidQuery)
		return
	}
	items, err := h.service.Suggest(r.Context(), clientIP(r, h.trustedProxies), values.Get("value"))
	if err != nil {
		writeCityError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cityPageJSON{Items: items})
}

func writeCityError(w http.ResponseWriter, r *http.Request, err error) {
	var limited *appbuyer.RegionLimitError
	if errors.As(err, &limited) {
		seconds := max(1, int(math.Ceil(limited.RetryAfter.Seconds())))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
	}
	writeError(w, r, err)
}
