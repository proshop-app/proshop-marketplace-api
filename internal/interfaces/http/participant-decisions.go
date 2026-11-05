package http

import (
	"net/http"
	"strings"
	"unicode/utf8"

	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type participantDecisionJSON struct {
	Trust  string `json:"trust"`
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

func (h *participantHandlers) decision(w http.ResponseWriter, r *http.Request) {
	var body participantDecisionJSON
	if r.ContentLength != 0 || r.Method == http.MethodPut {
		if err := readJSON(w, r, &body); err != nil {
			writeError(w, r, err)
			return
		}
	}
	if r.Method == http.MethodPut && (strings.TrimSpace(body.Note) == "" || utf8.RuneCountInString(body.Note) > 2000) {
		writeError(w, r, moderation.ErrInvalid)
		return
	}
	decision := participant.Decision{
		Operator: principalFrom(r),
		Reason:   body.Reason,
		Note:     strings.TrimSpace(body.Note),
	}
	h.withTenant(w, r, func(id ids.TenantID) (appparticipant.View, error) {
		if r.Method == http.MethodPut {
			return h.service.SetTrust(r.Context(), id, body.Trust, decision)
		}
		if strings.HasSuffix(r.URL.Path, "/enable") {
			return h.service.Enable(r.Context(), id, decision)
		}
		return h.service.Disable(r.Context(), id, decision)
	})
}

func (h *participantHandlers) decisions(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseTenantID(r.PathValue("tenantId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	f, err := moderationFilter(r, false)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.service.ListDecisions(r.Context(), id, participant.Page{Limit: f.Limit, Offset: f.Offset})
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]any, 0, len(page.Items))
	for _, d := range page.Items {
		items = append(items, map[string]any{
			"operator": moderation.Actor{ID: d.Operator.ID, Name: d.Operator.Name},
			"action":   d.Action,
			"value":    d.Value,
			"reason":   moderation.DecisionReason(d.Reason, d.ReasonMessage),
			"by":       d.Actor,
			"note":     d.Note,
			"at":       d.At,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"total":  page.Total,
		"limit":  page.Limit,
		"offset": page.Offset,
	})
}
