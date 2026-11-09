package http

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	apppromotion "github.com/proshop-app/proshop-marketplace/internal/application/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type promotionHandlers struct {
	service        *apppromotion.Service
	trustedProxies int
}

type promotionTargetJSON struct {
	OfferID    string `json:"offerId"`
	CategoryID string `json:"categoryId,omitempty"`
	SaleID     string `json:"saleId,omitempty"`
	StoreID    string `json:"storeId,omitempty"`
}

type promotionInput struct {
	eridSupplied  bool
	Title         string              `json:"title"`
	TenantID      ids.TenantID        `json:"tenantId"`
	Kind          promotion.Kind      `json:"kind"`
	PlacementCode string              `json:"placementCode"`
	Position      int                 `json:"position"`
	Target        promotionTargetJSON `json:"target"`
	Period        promotion.Period    `json:"period"`
	PriceKopecks  int64               `json:"priceKopecks"`
	Marking       promotion.Marking   `json:"marking"`
	OfferID       string              `json:"offerId,omitempty"`
	From          string              `json:"from,omitempty"`
	Weeks         int                 `json:"weeks,omitempty"`
}

type promotionOperatorInput struct {
	Title         string              `json:"title"`
	TenantID      ids.TenantID        `json:"tenantId"`
	Kind          promotion.Kind      `json:"kind"`
	PlacementCode string              `json:"placementCode"`
	Position      int                 `json:"position"`
	Target        promotionTargetJSON `json:"target"`
	Period        promotion.Period    `json:"period"`
	Marking       promotion.Marking   `json:"marking"`
}

type promotionTenantInput struct {
	Title         string            `json:"title"`
	PlacementCode string            `json:"placementCode"`
	Position      int               `json:"position"`
	OfferID       string            `json:"offerId"`
	From          string            `json:"from"`
	Weeks         int               `json:"weeks"`
	Marking       promotion.Marking `json:"marking"`
}

type promotionRefundJSON struct {
	AmountKopecks int64  `json:"amountKopecks"`
	On            string `json:"on"`
	Document      string `json:"document,omitempty"`
	Note          string `json:"note,omitempty"`
}

type promotionPaymentJSON struct {
	Refund        *promotionRefundJSON    `json:"refund,omitempty"`
	Status        promotion.PaymentStatus `json:"status"`
	PaidOn        string                  `json:"paidOn,omitempty"`
	AmountKopecks int64                   `json:"amountKopecks,omitempty"`
	Document      string                  `json:"document,omitempty"`
	Note          string                  `json:"note,omitempty"`
}

type promotionSummary struct {
	Impressions int64   `json:"impressions"`
	Clicks      int64   `json:"clicks"`
	CTR         float64 `json:"ctr"`
}

type campaignJSON struct {
	Title           string               `json:"title"`
	TenantID        ids.TenantID         `json:"tenantId"`
	Kind            promotion.Kind       `json:"kind"`
	PlacementCode   string               `json:"placementCode"`
	Position        int                  `json:"position"`
	Target          promotionTargetJSON  `json:"target"`
	Period          promotion.Period     `json:"period"`
	PriceKopecks    int64                `json:"priceKopecks"`
	Marking         promotion.Marking    `json:"marking"`
	ID              ids.UUID             `json:"id"`
	TenantName      string               `json:"tenantName"`
	Payment         promotionPaymentJSON `json:"payment"`
	Status          promotion.Status     `json:"status"`
	Version         int64                `json:"version"`
	CreatedAt       time.Time            `json:"createdAt"`
	UpdatedAt       time.Time            `json:"updatedAt"`
	RejectionReason string               `json:"rejectionReason,omitempty"`
	Stats           promotionSummary     `json:"stats"`
}

type promotionPlacementJSON struct {
	Code                string          `json:"code"`
	Surface             string          `json:"surface"`
	Title               string          `json:"title"`
	Positions           []int           `json:"positions"`
	SlotLimit           int             `json:"slotLimit"`
	PositionPrices      map[int]int64   `json:"positionPrices"`
	DefaultPriceKopecks int64           `json:"defaultPriceKopecks"`
	Active              bool            `json:"active"`
	Scope               promotion.Scope `json:"scope"`
	Version             int64           `json:"version"`
	ExpectedVersion     int64           `json:"expectedVersion,omitempty"`
}

type promotionDecisionInput struct {
	Refund          promotionRefundJSON  `json:"refund"`
	Action          promotion.Action     `json:"action"`
	ExpectedVersion int64                `json:"expectedVersion"`
	Note            string               `json:"note"`
	ERID            string               `json:"erid"`
	Payment         promotionPaymentJSON `json:"payment"`
}

type promotionPatch struct {
	ExpectedVersion int64           `json:"expectedVersion"`
	Changes         json.RawMessage `json:"changes"`
}

type sponsoredJSON struct {
	Position    int       `json:"position"`
	PromotionID ids.UUID  `json:"promotionId"`
	ERID        string    `json:"erid"`
	Offer       offerJSON `json:"offer"`
}

func decodePromotion(raw []byte, p promotionInput, tenant bool) (promotionInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if tenant {
		body := promotionTenantInput{
			Title:         p.Title,
			PlacementCode: p.PlacementCode,
			Position:      p.Position,
			OfferID:       p.OfferID,
			From:          p.From,
			Weeks:         p.Weeks,
			Marking:       p.Marking,
		}
		if err := decoder.Decode(&body); err != nil {
			return p, promotion.ErrInvalid
		}
		p.Title, p.PlacementCode, p.Position = body.Title, body.PlacementCode, body.Position
		p.OfferID, p.From, p.Weeks, p.Marking = body.OfferID, body.From, body.Weeks, body.Marking
	} else {
		body := promotionOperatorInput{
			Title:         p.Title,
			TenantID:      p.TenantID,
			Kind:          p.Kind,
			PlacementCode: p.PlacementCode,
			Position:      p.Position,
			Target:        p.Target,
			Period:        p.Period,
			Marking:       p.Marking,
		}
		if err := decoder.Decode(&body); err != nil {
			return p, promotion.ErrInvalid
		}
		p.Title, p.TenantID, p.Kind = body.Title, body.TenantID, body.Kind
		p.PlacementCode, p.Position, p.Target = body.PlacementCode, body.Position, body.Target
		p.Period, p.Marking = body.Period, body.Marking
	}
	var fields struct {
		Marking map[string]json.RawMessage `json:"marking"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return p, promotion.ErrInvalid
	}
	_, p.eridSupplied = fields.Marking["erid"]
	return p, nil
}

func refundResponse(r promotion.RefundRecord) *promotionRefundJSON {
	if r.On == "" {
		return nil
	}
	return &promotionRefundJSON{
		AmountKopecks: r.Amount.Kopecks(),
		On:            r.On,
		Document:      r.Document,
		Note:          r.Note,
	}
}

func serverAPI(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.NotFound(w, r)
			return
		}
		supplied, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(supplied)) != 1 {
			writeError(w, r, promotion.ErrUnauthorized)
			return
		}
		next(w, r)
	}
}

func promotionAccess(r *http.Request) apppromotion.Access {
	tenant := ids.TenantID(r.PathValue("tenantId"))
	actor := promotion.ActorOperator
	if tenant != "" {
		actor = promotion.ActorTenant
	}
	return apppromotion.Access{TenantID: tenant, Actor: actor}
}

func promotionID(r *http.Request) (ids.UUID, error) { return ids.ParseUUID(r.PathValue("id")) }

func (h *promotionHandlers) input(body promotionInput, tenant bool) (promotion.CampaignState, error) {
	if tenant && body.eridSupplied {
		return promotion.CampaignState{}, promotion.ErrInvalid
	}
	if body.Position == 0 {
		return promotion.CampaignState{}, promotion.ErrInvalid
	}
	offer := body.Target.OfferID
	if tenant {
		offer = body.OfferID
		body.Kind = promotion.SponsoredSlot
	}
	id, err := ids.ParseOfferID(offer)
	if err != nil {
		return promotion.CampaignState{}, err
	}
	if tenant {
		body.Period, err = h.service.Period(body.From, body.Weeks)
		if err != nil {
			return promotion.CampaignState{}, err
		}
	}
	return promotion.CampaignState{
		Title:         body.Title,
		TenantID:      body.TenantID,
		Kind:          body.Kind,
		PlacementCode: body.PlacementCode,
		Position:      body.Position,
		Target: promotion.Target{
			OfferID:    id,
			CategoryID: body.Target.CategoryID,
			SaleID:     body.Target.SaleID,
			StoreID:    body.Target.StoreID,
		},
		Period:  body.Period,
		Marking: body.Marking,
	}, nil
}

func campaignResponse(s promotion.CampaignState) campaignJSON {
	return campaignJSON{
		Title:           s.Title,
		TenantID:        s.TenantID,
		Kind:            s.Kind,
		PlacementCode:   s.PlacementCode,
		Position:        s.Position,
		Target:          promotionTargetJSON{OfferID: s.Target.OfferID.String(), CategoryID: s.Target.CategoryID},
		Period:          s.Period,
		PriceKopecks:    s.Price.Kopecks(),
		Marking:         s.Marking,
		RejectionReason: s.RejectionReason,
		ID:              s.ID,
		Status:          s.Status,
		Version:         s.Version,
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
		Payment: promotionPaymentJSON{
			Refund:        refundResponse(s.Payment.Refund),
			Status:        s.Payment.Status,
			PaidOn:        s.Payment.PaidOn,
			AmountKopecks: s.Payment.Amount.Kopecks(),
			Document:      s.Payment.Document,
			Note:          s.Payment.Note,
		},
	}
}

func promotionResponse(state apppromotion.Campaign) campaignJSON {
	body := campaignResponse(state.CampaignState)
	body.TenantName = state.Summary.TenantName
	body.Stats.Impressions = state.Summary.Impressions
	body.Stats.Clicks = state.Summary.Clicks
	if body.Stats.Impressions > 0 {
		body.Stats.CTR = 100 * float64(body.Stats.Clicks) / float64(body.Stats.Impressions)
	}
	return body
}

func (h *promotionHandlers) campaign(w http.ResponseWriter, r *http.Request) {
	id, err := promotionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	state, err := h.service.Get(r.Context(), promotionAccess(r), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := campaignMoneyResponse(r, state)
	writeJSON(w, http.StatusOK, body)
}

func (h *promotionHandlers) create(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := readJSON(w, r, &raw); err != nil {
		writeError(w, r, err)
		return
	}
	access := promotionAccess(r)
	body, err := decodePromotion(raw, promotionInput{}, access.TenantID != "")
	if err != nil {
		writeError(w, r, err)
		return
	}
	input, err := h.input(body, access.TenantID != "")
	if err != nil {
		writeError(w, r, err)
		return
	}
	state, err := h.service.Create(r.Context(), access, input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result := campaignMoneyResponse(r, state)
	writeJSON(w, http.StatusCreated, result)
}

func (h *promotionHandlers) edit(w http.ResponseWriter, r *http.Request) {
	id, err := promotionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var patch promotionPatch
	if err := readJSON(w, r, &patch); err != nil {
		writeError(w, r, err)
		return
	}
	access := promotionAccess(r)
	current, err := h.service.Get(r.Context(), access, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := promotionInput{
		Title:         current.Title,
		TenantID:      current.TenantID,
		Kind:          current.Kind,
		PlacementCode: current.PlacementCode,
		Position:      current.Position,
		Target: promotionTargetJSON{
			OfferID:    current.Target.OfferID.String(),
			CategoryID: current.Target.CategoryID,
		},
		Period:  current.Period,
		Marking: current.Marking,
	}
	if access.TenantID != "" {
		body.OfferID, body.From = current.Target.OfferID.String(), current.Period.From
		body.Weeks, err = h.service.Weeks(current.Period)
		if err != nil {
			writeError(w, r, err)
			return
		}
	}
	body, err = decodePromotion(patch.Changes, body, access.TenantID != "")
	if err != nil {
		writeError(w, r, promotion.ErrInvalid)
		return
	}
	input, err := h.input(body, access.TenantID != "")
	if err != nil {
		writeError(w, r, err)
		return
	}
	state, err := h.service.Edit(r.Context(), access, id, patch.ExpectedVersion, input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result := campaignMoneyResponse(r, state)
	writeJSON(w, http.StatusOK, result)
}

func (h *promotionHandlers) decide(w http.ResponseWriter, r *http.Request) {
	id, err := promotionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	body, err := readPromotionDecision(w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	access := promotionAccess(r)
	if access.TenantID != "" {
		switch {
		case strings.HasSuffix(r.URL.Path, "/submit"):
			body.Action = promotion.Submit
		case strings.HasSuffix(r.URL.Path, "/revise"):
			body.Action = promotion.Revise
		default:
			body.Action = promotion.Stop
		}
	}
	command := apppromotion.Command{
		Action:          body.Action,
		ExpectedVersion: body.ExpectedVersion,
		Note:            body.Note,
		ERID:            body.ERID,
		Payment: promotion.Payment{
			Refund: promotion.RefundRecord{
				Amount:   money.FromKopecks(body.Refund.AmountKopecks),
				On:       body.Refund.On,
				Document: body.Refund.Document,
				Note:     body.Refund.Note,
			},
			Amount:   money.FromKopecks(body.Payment.AmountKopecks),
			PaidOn:   body.Payment.PaidOn,
			Document: body.Payment.Document,
			Note:     body.Payment.Note,
		},
	}
	state, err := h.service.Decide(r.Context(), access, id, command)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result := campaignMoneyResponse(r, state)
	writeJSON(w, http.StatusOK, result)
}

func (h *promotionHandlers) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := intParam(q.Get("limit"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	offset, err := intParam(q.Get("offset"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	filter := promotion.Filter{
		ProductID: ids.ProductID(q.Get("productId")),
		Status:    promotion.Status(q.Get("status")),
		Kind:      promotion.Kind(q.Get("kind")),
		TenantID:  ids.TenantID(q.Get("tenantId")),
		From:      q.Get("from"),
		To:        q.Get("to"),
		Limit:     limit,
		Offset:    offset,
	}
	if promotionAccess(r).TenantID != "" {
		filter.Kind, filter.From, filter.To = "", "", ""
	}
	page, total, err := h.service.List(r.Context(), promotionAccess(r), filter)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := []any{}
	for _, state := range page.Items {
		body := campaignMoneyResponse(r, state)
		items = append(items, body)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"total":  total,
		"limit":  page.Limit,
		"offset": page.Offset,
		"counts": page.Counts,
	})
}

func (h *promotionHandlers) decisions(w http.ResponseWriter, r *http.Request) {
	id, err := promotionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := h.service.Decisions(r.Context(), promotionAccess(r), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *promotionHandlers) stats(w http.ResponseWriter, r *http.Request) {
	id, err := promotionID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	result, err := h.service.Stats(r.Context(), promotionAccess(r), id, q.Get("from"), q.Get("to"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func placementResponse(p promotion.PlacementState) promotionPlacementJSON {
	prices := map[int]int64{}
	for position, value := range p.PositionPrices {
		prices[position] = value.Kopecks()
	}
	return promotionPlacementJSON{
		Code:                p.Code,
		Surface:             p.Surface,
		Title:               p.Title,
		Positions:           p.Positions,
		SlotLimit:           p.SlotLimit,
		PositionPrices:      prices,
		DefaultPriceKopecks: p.DefaultPrice.Kopecks(),
		Active:              p.Active,
		Scope:               p.Scope,
		Version:             p.Version,
	}
}

func (h *promotionHandlers) placements(w http.ResponseWriter, r *http.Request) {
	states, err := h.service.Placements(r.Context(), promotionAccess(r).TenantID != "")
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := []any{}
	for _, state := range states {
		items = append(items, placementMoneyResponse(r, state))
	}
	if promotionAccess(r).TenantID != "" {
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "nextMonday": h.service.NextMonday()})
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *promotionHandlers) configure(w http.ResponseWriter, r *http.Request) {
	var body promotionPlacementRublesJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	prices := map[int]money.Money{}
	for position, value := range body.PositionPrices {
		prices[position] = money.FromKopecks(int64(value))
	}
	state, err := h.service.ConfigurePlacement(
		r.Context(),
		r.PathValue("code"),
		body.SlotLimit,
		money.FromKopecks(int64(body.DefaultPriceRubles)),
		prices,
		body.Active,
		body.ExpectedVersion,
	)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, placementMoneyResponse(r, state))
}

func (h *promotionHandlers) calendar(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	result, err := h.service.Calendar(r.Context(), r.PathValue("code"), q.Get("from"), q.Get("to"), q.Get("categoryId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *promotionHandlers) overview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	result, err := h.service.Overview(r.Context(), q.Get("from"), q.Get("to"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, overviewMoneyResponse(result))
}

func (h *promotionHandlers) availability(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	position, err := intParam(q.Get("position"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	weeks, err := intParam(q.Get("weeks"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := h.service.Availability(
		r.Context(),
		q.Get("placementCode"),
		position,
		q.Get("from"),
		weeks,
		q.Get("categoryId"),
	)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *promotionHandlers) sponsored(w http.ResponseWriter, r *http.Request) {
	query, err := parseOfferQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	query.BuyerID = BuyerID(r.Context())
	rows, err := h.service.Sponsored(r.Context(), query)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := []sponsoredJSON{}
	for _, row := range rows {
		offer := toOfferJSON(appcatalog.OfferCard{Offer: row.Offer}, false)
		annotateDelivery(&offer, r)
		items = append(items, sponsoredJSON{
			Position:    row.Position,
			PromotionID: row.PromotionID,
			ERID:        row.ERID,
			Offer:       offer,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *promotionHandlers) events(w http.ResponseWriter, r *http.Request) {
	var body struct {
		VisitorID string               `json:"visitorId"`
		Events    []apppromotion.Event `json:"events"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if body.VisitorID != "" {
		body.VisitorID = "visitor:" + body.VisitorID
	}
	if buyer := BuyerID(r.Context()); buyer != "" {
		body.VisitorID = "buyer:" + string(buyer)
	}
	if err := h.service.AllowEvents(r.Context(), clientIP(r, h.trustedProxies), len(body.Events)); err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.service.Events(r.Context(), body.VisitorID, body.Events); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *promotionHandlers) delete(w http.ResponseWriter, r *http.Request) {
	id, err := promotionID(r)
	if err == nil {
		err = h.service.Delete(r.Context(), promotionAccess(r), id)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
