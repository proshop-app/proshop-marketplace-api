package http

import (
	"net/http"
	"strings"

	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type moderationHandlers struct{ service *appmoderation.Service }

type moderationCaseJSON struct {
	moderation.CaseState
	OfferID string `json:"offerId"`
}

type moderationRequest struct {
	OfferID         string `json:"offerId"`
	ExpectedVersion int64  `json:"expectedVersion"`
	Reason          string `json:"reason"`
	Note            string `json:"note"`
	Text            string `json:"text"`
	Code            string `json:"code"`
	Message         string `json:"message"`
}

func caseJSON(c moderation.CaseState) moderationCaseJSON {
	c.Card = nil
	if c.Reasons == nil {
		c.Reasons = []moderation.Reason{}
	}
	return moderationCaseJSON{CaseState: c, OfferID: ids.OfferID{Tenant: c.TenantID, Product: c.ProductID}.String()}
}

func verdictJSON(v *moderation.VerdictState) any {
	if v == nil {
		return nil
	}
	reasons := v.Reasons
	if reasons == nil {
		reasons = []moderation.Reason{}
	}
	return map[string]any{
		"offerId":   ids.OfferID{Tenant: v.TenantID, Product: v.ProductID}.String(),
		"tenantId":  v.TenantID,
		"productId": v.ProductID,
		"revision":  v.Revision,
		"status":    v.Status,
		"reasons":   reasons,
		"decidedBy": v.DecidedBy,
		"updatedAt": v.UpdatedAt,
	}
}

func (h *moderationHandlers) cases(w http.ResponseWriter, r *http.Request) {
	f, err := moderationFilter(r, false)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.service.ListCases(r.Context(), f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]moderationCaseJSON, 0, len(page.Items))
	for _, c := range page.Items {
		items = append(items, caseJSON(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"total":  page.Total,
		"limit":  page.Limit,
		"offset": page.Offset,
	})
}

func (h *moderationHandlers) verdicts(w http.ResponseWriter, r *http.Request) {
	f, err := moderationFilter(r, true)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.service.ListVerdicts(r.Context(), f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]any, 0, len(page.Items))
	for _, v := range page.Items {
		items = append(items, verdictJSON(&v))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"total":  page.Total,
		"limit":  page.Limit,
		"offset": page.Offset,
	})
}

func moderationFilter(r *http.Request, verdict bool) (moderation.Filter, error) {
	q := r.URL.Query()
	f := moderation.Filter{Status: moderation.Status(q.Get("status")), Source: q.Get("source")}
	var err error
	f.Limit, err = intParam(q.Get("limit"))
	if err != nil {
		return f, moderation.ErrInvalid
	}
	f.Offset, err = intParam(q.Get("offset"))
	if err != nil || f.Offset < 0 || f.Limit < 0 || f.Limit > 200 {
		return f, moderation.ErrInvalid
	}
	if q.Get("tenantId") != "" {
		f.TenantID, err = ids.ParseTenantID(q.Get("tenantId"))
		if err != nil {
			return f, moderation.ErrInvalid
		}
	}
	if f.Source != "" && f.Source != moderation.Rule && f.Source != moderation.Operator {
		return f, moderation.ErrInvalid
	}
	if verdict {
		if f.Status != moderation.Allowed && f.Status != moderation.Pending && f.Status != moderation.Rejected {
			return f, moderation.ErrInvalid
		}
	} else {
		switch f.Status {
		case "":
			f.Status = moderation.Open
		case "all":
			f.Status = ""
		case moderation.Open, moderation.Allowed, moderation.Rejected, moderation.Superseded:
		default:
			return f, moderation.ErrInvalid
		}
	}
	return f, nil
}

func (h *moderationHandlers) getCase(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseUUID(r.PathValue("caseId"))
	if err != nil {
		writeError(w, r, moderation.ErrInvalid)
		return
	}
	view, err := h.service.CaseDetails(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"case":     caseJSON(view.Case),
		"offer":    view.Offer,
		"decision": decisionJSON(view.Decision),
		"history":  decisionHistory(view.History),
	})
}

func (h *moderationHandlers) open(w http.ResponseWriter, r *http.Request) {
	var body moderationRequest
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	id, err := ids.ParseOfferID(body.OfferID)
	if err != nil {
		writeError(w, r, moderation.ErrInvalid)
		return
	}
	c, err := h.service.OpenCase(r.Context(), id, body.Note)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, caseJSON(c))
}

func (h *moderationHandlers) decide(w http.ResponseWriter, r *http.Request) {
	var body moderationRequest
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if body.ExpectedVersion < 1 {
		writeError(w, r, moderation.ErrInvalid)
		return
	}
	id, err := ids.ParseUUID(r.PathValue("caseId"))
	if err != nil {
		writeError(w, r, moderation.ErrInvalid)
		return
	}
	principal := principalFrom(r)
	cmd := appmoderation.DecisionCommand{
		CaseID:          id,
		ExpectedVersion: body.ExpectedVersion,
		Reason:          body.Reason,
		Note:            body.Note,
		Operator:        moderation.Actor{ID: principal.ID, Name: principal.Name},
		IdempotencyKey:  r.Header.Get("Idempotency-Key"),
	}
	decide := h.service.Allow
	if strings.HasSuffix(r.URL.Path, "/reject") {
		decide = h.service.Reject
	}
	c, err := decide(r.Context(), cmd)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, caseJSON(c))
}

func (h *moderationHandlers) offer(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseOfferID(r.PathValue("offerId"))
	if err != nil {
		writeError(w, r, moderation.ErrInvalid)
		return
	}
	view, err := h.service.GetOffer(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var c *moderationCaseJSON
	if view.Case != nil {
		value := caseJSON(*view.Case)
		c = &value
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"verdict": verdictJSON(view.Verdict),
		"case":    c,
		"history": decisionHistory(view.History),
	})
}

func (h *moderationHandlers) phrases(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.ListStopPhrases(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	if items == nil {
		items = []moderation.StopPhrase{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *moderationHandlers) addPhrase(w http.ResponseWriter, r *http.Request) {
	var body moderationRequest
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	principal := principalFrom(r)
	actor := moderation.Actor{ID: principal.ID, Name: principal.Name}
	phrase, err := h.service.CreateStopPhrase(r.Context(), body.Text, actor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, phrase)
}

func (h *moderationHandlers) removePhrase(w http.ResponseWriter, r *http.Request) {
	if err := h.service.RemoveStopPhrase(r.Context(), r.PathValue("key")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *moderationHandlers) reasons(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.ListReasons(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	if items == nil {
		items = []moderation.ReasonCode{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *moderationHandlers) putReason(w http.ResponseWriter, r *http.Request) {
	var body moderationRequest
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	reason := moderation.ReasonCode{Code: r.PathValue("code"), Text: body.Message}
	if err := h.service.PutReason(r.Context(), reason); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, reason)
}

func (h *moderationHandlers) addReason(w http.ResponseWriter, r *http.Request) {
	var body moderationRequest
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	reason := moderation.ReasonCode{Code: body.Code, Text: body.Message}
	if err := h.service.AddReason(r.Context(), reason); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, reason)
}

func decisionHistory(history []moderation.Decision) []any {
	result := make([]any, 0, len(history))
	for _, d := range history {
		result = append(result, decisionJSON(&d))
	}
	return result
}

func decisionJSON(d *moderation.Decision) any {
	if d == nil {
		return nil
	}
	return map[string]any{
		"id":       d.ID,
		"caseId":   d.CaseID,
		"operator": d.Operator,
		"action":   d.Action,
		"reason":   moderation.DecisionReason(d.Reason, d.ReasonMessage),
		"note":     d.Note,
		"revision": d.Revision,
		"at":       d.At,
	}
}
