package http

import (
	"net/http"
	"net/url"
	"strings"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const maxOfferIDs = 20

func (h *catalogHandlers) listIDs(w http.ResponseWriter, r *http.Request) {
	offerIDs, err := parseOfferIDs(r.URL.Query())
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := validateDeliveryContext(r); err != nil {
		writeError(w, r, err)
		return
	}
	cards, err := h.queries.Offers(r.Context(), offerIDs, BuyerID(r.Context()))
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := offerPageJSON{
		Facets: []facetJSON{},
		Items:  make([]offerJSON, 0, len(cards)),
		Total:  len(cards),
		Limit:  len(offerIDs),
	}
	for _, card := range cards {
		item := toOfferJSON(card, false)
		annotateDelivery(&item, r)
		body.Items = append(body.Items, item)
	}
	writeJSON(w, http.StatusOK, body)
}

func parseOfferIDs(values url.Values) ([]ids.OfferID, error) {
	for key := range values {
		switch key {
		case "q", "tenant", "groupId", "category", "priceMin", "priceMax", "inStock":
			return nil, appcatalog.ErrInvalidQuery
		case "sort", "limit", "offset", "track":
			return nil, appcatalog.ErrInvalidQuery
		}
		if strings.HasPrefix(key, "attr.") {
			return nil, appcatalog.ErrInvalidQuery
		}
	}
	if len(values["ids"]) == 0 || len(values["ids"]) > maxOfferIDs {
		return nil, appcatalog.ErrInvalidQuery
	}
	offerIDs := make([]ids.OfferID, 0, len(values["ids"]))
	seen := make(map[ids.OfferID]bool)
	for _, raw := range values["ids"] {
		id, err := ids.ParseOfferID(raw)
		if err != nil {
			return nil, appcatalog.ErrInvalidQuery
		}
		if !seen[id] {
			offerIDs = append(offerIDs, id)
			seen[id] = true
		}
	}
	return offerIDs, nil
}
