package http

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/searchfeedback"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const searchBuyerTimeout = 75 * time.Millisecond

type searchHandlers struct {
	feedback       *searchfeedback.Feedback
	keys           *searchfeedback.BuyerKeys
	auth           *appbuyer.Auth
	trustedProxies int
}

type searchFeedbackInput struct {
	Kind     string          `json:"kind"`
	Receipt  string          `json:"receipt"`
	OfferID  json.RawMessage `json:"offerId"`
	OfferIDs json.RawMessage `json:"offerIds"`
}

type searchFeedbackResponse struct {
	Data bool `json:"data"`
}

// buyerKey использует только access. Выделенный бюджет оставляет время на саму выдачу.
func (h *searchHandlers) buyerKey(r *http.Request) string {
	token := sessionToken(r)
	if token == "" || h.auth == nil || h.keys == nil {
		return ""
	}
	budget := searchBuyerTimeout
	if deadline, ok := r.Context().Deadline(); ok {
		budget = min(budget, time.Until(deadline)/4)
	}
	if budget <= 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(r.Context(), budget)
	defer cancel()
	session, err := h.auth.Authenticate(ctx, token)
	if err != nil {
		return ""
	}
	return h.keys.Key(session.BuyerID)
}

func (h *searchHandlers) record(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	event, err := readSearchFeedback(w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var accepted bool
	if h.feedback != nil && h.feedback.Allow(r.Context(), clientIP(r, h.trustedProxies)) {
		if event.Kind == searchfeedback.Click {
			event.BuyerKey = h.buyerKey(r)
		}
		accepted = h.feedback.Record(r.Context(), event)
	}
	writeJSON(w, http.StatusOK, searchFeedbackResponse{Data: accepted})
}

func readSearchFeedback(w http.ResponseWriter, r *http.Request) (ports.SearchFeedback, error) {
	var body searchFeedbackInput
	if err := decodeLimited(w, r, &body, searchfeedback.MaxBodyBytes); err != nil {
		return ports.SearchFeedback{}, err
	}
	if !searchfeedback.ValidReceipt(body.Receipt) {
		return ports.SearchFeedback{}, searchfeedback.ErrInvalid
	}
	event := ports.SearchFeedback{Kind: body.Kind, Receipt: body.Receipt}
	switch body.Kind {
	case searchfeedback.Click:
		if body.OfferIDs != nil || json.Unmarshal(body.OfferID, &event.OfferID) != nil || !validOfferID(event.OfferID) {
			return ports.SearchFeedback{}, searchfeedback.ErrInvalid
		}
	case searchfeedback.Impression:
		if body.OfferID != nil || json.Unmarshal(body.OfferIDs, &event.OfferIDs) != nil ||
			len(event.OfferIDs) == 0 || len(event.OfferIDs) > searchfeedback.MaxImpressions {
			return ports.SearchFeedback{}, searchfeedback.ErrInvalid
		}
		for _, id := range event.OfferIDs {
			if !validOfferID(id) {
				return ports.SearchFeedback{}, searchfeedback.ErrInvalid
			}
		}
	default:
		return ports.SearchFeedback{}, searchfeedback.ErrInvalid
	}
	return event, nil
}

func validOfferID(id string) bool {
	_, err := ids.ParseOfferID(id)
	return err == nil
}
