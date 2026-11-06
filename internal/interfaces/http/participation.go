package http

import (
	"net/http"
	"time"

	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type participationDecisionJSON struct {
	Reason   *moderation.ReasonCode `json:"reason"`
	Note     string                 `json:"note"`
	At       time.Time              `json:"at"`
	Operator moderation.Actor       `json:"operator"`
}

type participationJSON struct {
	Stage          participant.Stage          `json:"stage"`
	AwaitingReview bool                       `json:"awaitingReview"`
	DeliveryValid  bool                       `json:"deliveryValid"`
	LastDecision   *participationDecisionJSON `json:"lastDecision"`
}

type returnParticipantJSON struct {
	Reason *moderation.ReasonCode `json:"reason"`
	Note   string                 `json:"note"`
}

func toParticipationDecision(d *participant.LastDecision) *participationDecisionJSON {
	if d == nil {
		return nil
	}
	return &participationDecisionJSON{
		Reason:   moderation.DecisionReason(d.Reason, d.ReasonMessage),
		Note:     d.Note,
		At:       d.At,
		Operator: moderation.Actor{ID: d.Operator.ID, Name: d.Operator.Name},
	}
}

func (h *participantHandlers) participation(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseTenantID(r.PathValue("tenantId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	var view appparticipant.View
	if r.Method == http.MethodPost {
		view, err = h.service.Submit(r.Context(), id)
	} else {
		view, err = h.service.Get(r.Context(), id)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	p := view.Participant
	writeJSON(w, http.StatusOK, participationJSON{
		Stage:          p.Stage,
		AwaitingReview: p.AwaitingReview,
		DeliveryValid:  view.DeliveryValid,
		LastDecision:   toParticipationDecision(p.LastDecision),
	})
}

func (h *participantHandlers) returnParticipant(w http.ResponseWriter, r *http.Request) {
	var body returnParticipantJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	id, err := ids.ParseTenantID(r.PathValue("tenantId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	decision := participant.Decision{Operator: principalFrom(r), Note: body.Note}
	if body.Reason != nil {
		if body.Reason.Code == "" {
			writeError(w, r, ErrInvalidBody)
			return
		}
		decision.Reason = body.Reason.Code
	}
	view, err := h.service.Return(r.Context(), id, decision)
	respond(w, r, http.StatusOK, view, err)
}
