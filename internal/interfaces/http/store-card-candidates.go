package http

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func parseCardCandidatesQuery(values url.Values) (appparticipant.CardCandidatesQuery, error) {
	query := appparticipant.CardCandidatesQuery{
		Page:  1,
		Limit: appparticipant.CardCandidatesPageSize,
	}
	for key, raw := range values {
		if len(raw) != 1 {
			return query, ErrInvalidQuery
		}
		switch key {
		case "query":
			query.Query = raw[0]
		case "ids":
			query.IDs = strings.Split(raw[0], ",")
		case "page", "limit":
			value, err := strconv.Atoi(raw[0])
			if err != nil || value < 1 {
				return query, ErrInvalidQuery
			}
			if key == "page" {
				query.Page = value
			} else {
				query.Limit = value
			}
		default:
			return query, ErrInvalidQuery
		}
	}
	return query, nil
}

func (h *participantHandlers) cardCandidates(w http.ResponseWriter, r *http.Request) {
	tenant, err := ids.ParseTenantID(r.PathValue("tenantId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	query, err := parseCardCandidatesQuery(r.URL.Query())
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := h.service.CardCandidates(r.Context(), tenant, query)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
