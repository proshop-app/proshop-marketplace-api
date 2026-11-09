package http

import (
	"net/http"
	"strings"
	"time"

	apppromotion "github.com/proshop-app/proshop-marketplace/internal/application/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Отдельные DTO сохраняют служебный контракт магазина в целых копейках.
type promotionRefundRublesJSON struct {
	AmountRubles rubles `json:"amountRubles"`
	On           string `json:"on"`
	Document     string `json:"document,omitempty"`
	Note         string `json:"note,omitempty"`
}

type promotionPaymentRublesJSON struct {
	Refund       *promotionRefundRublesJSON `json:"refund,omitempty"`
	Status       promotion.PaymentStatus    `json:"status"`
	PaidOn       string                     `json:"paidOn,omitempty"`
	AmountRubles rubles                     `json:"amountRubles,omitempty"`
	Document     string                     `json:"document,omitempty"`
	Note         string                     `json:"note,omitempty"`
}

type campaignRublesJSON struct {
	Title           string                     `json:"title"`
	TenantID        ids.TenantID               `json:"tenantId"`
	Kind            promotion.Kind             `json:"kind"`
	PlacementCode   string                     `json:"placementCode"`
	Position        int                        `json:"position"`
	Target          promotionTargetJSON        `json:"target"`
	Period          promotion.Period           `json:"period"`
	PriceRubles     rubles                     `json:"priceRubles"`
	Marking         promotion.Marking          `json:"marking"`
	ID              ids.UUID                   `json:"id"`
	TenantName      string                     `json:"tenantName"`
	Payment         promotionPaymentRublesJSON `json:"payment"`
	Status          promotion.Status           `json:"status"`
	Version         int64                      `json:"version"`
	CreatedAt       time.Time                  `json:"createdAt"`
	UpdatedAt       time.Time                  `json:"updatedAt"`
	RejectionReason string                     `json:"rejectionReason,omitempty"`
	Stats           promotionSummary           `json:"stats"`
}

type promotionPlacementRublesJSON struct {
	Code               string          `json:"code"`
	Surface            string          `json:"surface"`
	Title              string          `json:"title"`
	Positions          []int           `json:"positions"`
	SlotLimit          int             `json:"slotLimit"`
	PositionPrices     map[int]rubles  `json:"positionPrices"`
	DefaultPriceRubles rubles          `json:"defaultPriceRubles"`
	Active             bool            `json:"active"`
	Scope              promotion.Scope `json:"scope"`
	Version            int64           `json:"version"`
	ExpectedVersion    int64           `json:"expectedVersion,omitempty"`
}

type promotionDecisionRublesInput struct {
	Refund          promotionRefundRublesJSON  `json:"refund"`
	Action          promotion.Action           `json:"action"`
	ExpectedVersion int64                      `json:"expectedVersion"`
	Note            string                     `json:"note"`
	ERID            string                     `json:"erid"`
	Payment         promotionPaymentRublesJSON `json:"payment"`
}

type promotionRevenueDayJSON struct {
	Day    string `json:"day"`
	Rubles rubles `json:"rubles"`
}

type promotionOverviewJSON struct {
	Period          promotion.Period             `json:"period"`
	RevenueRubles   rubles                       `json:"revenueRubles"`
	ActiveCampaigns int                          `json:"activeCampaigns"`
	TotalCampaigns  int                          `json:"totalCampaigns"`
	Fill            apppromotion.Fill            `json:"fill"`
	Placements      []apppromotion.PlacementFill `json:"placements"`
	ReviewQueue     int                          `json:"reviewQueue"`
	RevenueByDay    []promotionRevenueDayJSON    `json:"revenueByDay"`
	TrafficByDay    []apppromotion.TrafficDay    `json:"trafficByDay"`
}

func internalMoneyContract(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/v1/internal/")
}

func campaignMoneyResponse(r *http.Request, state apppromotion.Campaign) any {
	legacy := promotionResponse(state)
	if internalMoneyContract(r) {
		return legacy
	}
	payment := promotionPaymentRublesJSON{
		Status:       legacy.Payment.Status,
		PaidOn:       legacy.Payment.PaidOn,
		AmountRubles: rubles(legacy.Payment.AmountKopecks),
		Document:     legacy.Payment.Document,
		Note:         legacy.Payment.Note,
	}
	if refund := legacy.Payment.Refund; refund != nil {
		payment.Refund = &promotionRefundRublesJSON{
			AmountRubles: rubles(refund.AmountKopecks),
			On:           refund.On,
			Document:     refund.Document,
			Note:         refund.Note,
		}
	}
	return campaignRublesJSON{
		Title:           legacy.Title,
		TenantID:        legacy.TenantID,
		Kind:            legacy.Kind,
		PlacementCode:   legacy.PlacementCode,
		Position:        legacy.Position,
		Target:          legacy.Target,
		Period:          legacy.Period,
		PriceRubles:     rubles(legacy.PriceKopecks),
		Marking:         legacy.Marking,
		ID:              legacy.ID,
		TenantName:      legacy.TenantName,
		Payment:         payment,
		Status:          legacy.Status,
		Version:         legacy.Version,
		CreatedAt:       legacy.CreatedAt,
		UpdatedAt:       legacy.UpdatedAt,
		RejectionReason: legacy.RejectionReason,
		Stats:           legacy.Stats,
	}
}

func placementMoneyResponse(r *http.Request, state promotion.PlacementState) any {
	if internalMoneyContract(r) {
		return placementResponse(state)
	}
	prices := make(map[int]rubles, len(state.PositionPrices))
	for position, value := range state.PositionPrices {
		prices[position] = rubles(value.Kopecks())
	}
	return promotionPlacementRublesJSON{
		Code:               state.Code,
		Surface:            state.Surface,
		Title:              state.Title,
		Positions:          state.Positions,
		SlotLimit:          state.SlotLimit,
		PositionPrices:     prices,
		DefaultPriceRubles: rubles(state.DefaultPrice.Kopecks()),
		Active:             state.Active,
		Scope:              state.Scope,
		Version:            state.Version,
	}
}

func overviewMoneyResponse(state apppromotion.Overview) promotionOverviewJSON {
	days := make([]promotionRevenueDayJSON, 0, len(state.RevenueByDay))
	for _, day := range state.RevenueByDay {
		days = append(days, promotionRevenueDayJSON{Day: day.Day, Rubles: rubles(day.Kopecks)})
	}
	return promotionOverviewJSON{
		Period:          state.Period,
		RevenueRubles:   rubles(state.RevenueKopecks),
		ActiveCampaigns: state.ActiveCampaigns,
		TotalCampaigns:  state.TotalCampaigns,
		Fill:            state.Fill,
		Placements:      state.Placements,
		ReviewQueue:     state.ReviewQueue,
		RevenueByDay:    days,
		TrafficByDay:    state.TrafficByDay,
	}
}

func readPromotionDecision(w http.ResponseWriter, r *http.Request) (promotionDecisionInput, error) {
	var result promotionDecisionInput
	if internalMoneyContract(r) {
		err := readJSON(w, r, &result)
		return result, err
	}
	var body promotionDecisionRublesInput
	if err := readJSON(w, r, &body); err != nil {
		return result, err
	}
	result = promotionDecisionInput{
		Action:          body.Action,
		ExpectedVersion: body.ExpectedVersion,
		Note:            body.Note,
		ERID:            body.ERID,
		Refund: promotionRefundJSON{
			AmountKopecks: int64(body.Refund.AmountRubles),
			On:            body.Refund.On,
			Document:      body.Refund.Document,
			Note:          body.Refund.Note,
		},
		Payment: promotionPaymentJSON{
			AmountKopecks: int64(body.Payment.AmountRubles),
			PaidOn:        body.Payment.PaidOn,
			Document:      body.Payment.Document,
			Note:          body.Payment.Note,
		},
	}
	return result, nil
}
