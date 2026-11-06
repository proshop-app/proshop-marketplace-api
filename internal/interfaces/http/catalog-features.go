package http

import (
	"net/http"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type recommendationsJSON struct {
	Items  []offerJSON `json:"items"`
	Reason string      `json:"reason"`
}

func (h *catalogHandlers) reviews(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseOfferID(r.PathValue("offerId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	limit, err := intParam(r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, r, appcatalog.ErrInvalidQuery.WithMessage("limit должен быть целым числом"))
		return
	}
	offset, err := intParam(r.URL.Query().Get("offset"))
	if err != nil {
		writeError(w, r, appcatalog.ErrInvalidQuery.WithMessage("offset должен быть целым числом"))
		return
	}
	page, err := h.queries.Reviews(r.Context(), id, limit, offset)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	writeJSON(w, http.StatusOK, page)
}

func (h *catalogHandlers) recommendations(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	request := appcatalog.RecommendationRequest{
		Context: values.Get("context"),
		BuyerID: BuyerID(r.Context()),
		Region:  values.Get("deliveryRegion"),
	}
	var err error
	request.Limit, err = intParam(values.Get("limit"))
	if err != nil {
		writeError(w, r, appcatalog.ErrInvalidQuery.WithMessage("limit должен быть целым числом"))
		return
	}
	if request.Context == "offer" {
		request.OfferID, err = ids.ParseOfferID(values.Get("offerId"))
		if err != nil {
			writeError(w, r, err)
			return
		}
	} else if values.Get("offerId") != "" {
		writeError(w, r, appcatalog.ErrInvalidQuery.WithMessage("offerId допустим только для context=offer"))
		return
	}
	result, err := h.queries.Recommend(r.Context(), request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := recommendationsJSON{Items: []offerJSON{}, Reason: result.Reason}
	for _, item := range result.Items {
		body.Items = append(body.Items, toOfferJSON(item, false))
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, body)
}
