package http

import (
	"net/http"
	"strconv"
	"time"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// participantJSON — участник в операторском ответе.
type participantJSON struct {
	Card              operatorCardJSON             `json:"card"`
	CardReady         bool                         `json:"cardReady"`
	CardMissing       []string                     `json:"cardMissing"`
	AwaitingReview    bool                         `json:"awaitingReview"`
	DeliveryValid     bool                         `json:"deliveryValid"`
	LastDecision      *participationDecisionJSON   `json:"lastDecision"`
	ProbationOffers   int                          `json:"probationOffers"`
	Trust             string                       `json:"trust"`
	ApprovedOffers    int                          `json:"approvedOffers"`
	TenantID          string                       `json:"tenantId"`
	Slug              string                       `json:"slug"`
	Name              string                       `json:"name"`
	Stage             string                       `json:"stage"`
	Enabled           bool                         `json:"enabled"`
	Subsidy           subsidyJSON                  `json:"subsidy"`
	FreeDeliveryFrom  rubles                       `json:"freeDeliveryFrom"`
	Delivery          participant.DeliverySettings `json:"delivery"`
	SettingsSyncedAt  *time.Time                   `json:"settingsSyncedAt"`
	TenantStatus      string                       `json:"tenantStatus"`
	Visible           bool                         `json:"visible"`
	CheckoutAvailable bool                         `json:"checkoutAvailable"`
	Version           int64                        `json:"version"`
	CreatedAt         time.Time                    `json:"createdAt"`
	UpdatedAt         time.Time                    `json:"updatedAt"`
}

// subsidyJSON — субсидия доставки продавца: value — рубли для fixed, процент для percent.
type subsidyJSON struct {
	Enabled   bool   `json:"enabled"`
	Type      string `json:"type,omitempty"`
	Value     any    `json:"value"`
	MaxAmount rubles `json:"maxAmount"`
}

type participantListJSON struct {
	Items  []participantJSON `json:"items"`
	Total  int               `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

type participantHandlers struct {
	buyerFeatures *appbuyer.Features
	service       *appparticipant.Service
}

type createParticipantJSON struct {
	TenantID string `json:"tenantId"`
	Name     string `json:"name"`
}

type operatorCardJSON struct {
	Logo   *string                  `json:"logo"`
	Hidden []participant.CardHidden `json:"hidden"`
}

type participantDeliveryJSON struct {
	Version  int64                        `json:"version"`
	Delivery participant.DeliverySettings `json:"delivery"`
}

func toParticipantJSON(view appparticipant.View) participantJSON {
	p := view.Participant
	out := participantJSON{
		Card: operatorCardJSON{
			Logo:   p.StoreCard.Profile.Logo,
			Hidden: append([]participant.CardHidden{}, p.StoreCard.Hidden...),
		},
		CardReady:         p.StoreCard.Readiness.Ready,
		CardMissing:       append([]string{}, p.StoreCard.Readiness.Missing...),
		AwaitingReview:    p.AwaitingReview,
		DeliveryValid:     view.DeliveryValid,
		LastDecision:      toParticipationDecision(p.LastDecision),
		ProbationOffers:   view.ProbationOffers,
		Trust:             p.Trust,
		ApprovedOffers:    p.ApprovedOffers,
		TenantID:          string(p.TenantID),
		FreeDeliveryFrom:  rubles(p.FreeDeliveryFrom.Kopecks()),
		Delivery:          p.Delivery,
		Slug:              p.Slug,
		Name:              p.Name,
		Stage:             string(p.Stage),
		Enabled:           p.Enabled,
		TenantStatus:      string(view.TenantStatus),
		Visible:           view.Visibility.Visible,
		CheckoutAvailable: view.Visibility.CheckoutAvailable,
		Version:           p.Version,
		CreatedAt:         p.CreatedAt.UTC(),
		UpdatedAt:         p.UpdatedAt.UTC(),
		Subsidy: subsidyJSON{
			Enabled:   p.Subsidy.Enabled,
			Type:      string(p.Subsidy.Type),
			Value:     subsidyValueResponse(p.Subsidy),
			MaxAmount: rubles(p.Subsidy.MaxAmount.Kopecks()),
		},
	}
	if !p.SettingsSyncedAt.IsZero() {
		synced := p.SettingsSyncedAt.UTC()
		out.SettingsSyncedAt = &synced
	}

	return out
}

func (h *participantHandlers) list(w http.ResponseWriter, r *http.Request) {
	page := participant.Page{
		Limit: appparticipant.MaxPageLimit,
		Trust: r.URL.Query().Get("trust"),
		Stage: participant.Stage(r.URL.Query().Get("stage")),
	}
	if raw := r.URL.Query().Get("queue"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, r, ErrInvalidQuery)
			return
		}
		page.Queue = value
	}

	for name, target := range map[string]*int{"limit": &page.Limit, "offset": &page.Offset} {
		if raw := r.URL.Query().Get(name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, r, ErrInvalidQuery.WithMessage(name+" — целое число"))

				return
			}

			*target = value
		}
	}

	list, err := h.service.List(r.Context(), page)
	if err != nil {
		writeError(w, r, err)

		return
	}

	if page.Limit <= 0 || page.Limit > appparticipant.MaxPageLimit {
		page.Limit = appparticipant.MaxPageLimit
	}

	body := participantListJSON{
		Items:  make([]participantJSON, 0, len(list.Items)),
		Total:  list.Total,
		Limit:  page.Limit,
		Offset: max(page.Offset, 0),
	}
	for _, view := range list.Items {
		body.Items = append(body.Items, toParticipantJSON(view))
	}

	writeJSON(w, http.StatusOK, body)
}

func (h *participantHandlers) create(w http.ResponseWriter, r *http.Request) {
	var body createParticipantJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)

		return
	}

	tenantID, err := ids.ParseTenantID(body.TenantID)
	if err != nil {
		writeError(w, r, err)

		return
	}

	view, err := h.service.Create(r.Context(), appparticipant.CreateCommand{
		TenantID: tenantID,
		Name:     body.Name,
		Operator: principalFrom(r),
	})
	respond(w, r, http.StatusCreated, view, err)
}

func (h *participantHandlers) get(w http.ResponseWriter, r *http.Request) {
	h.withTenant(w, r, func(tenantID ids.TenantID) (appparticipant.View, error) {
		return h.service.Get(r.Context(), tenantID)
	})
}

func (h *participantHandlers) withTenant(
	w http.ResponseWriter,
	r *http.Request,
	run func(ids.TenantID) (appparticipant.View, error),
) {
	tenantID, err := ids.ParseTenantID(r.PathValue("tenantId"))
	if err != nil {
		writeError(w, r, err)

		return
	}

	view, err := run(tenantID)
	respond(w, r, http.StatusOK, view, err)
}

func respond(w http.ResponseWriter, r *http.Request, status int, view appparticipant.View, err error) {
	if err != nil {
		writeError(w, r, err)

		return
	}

	writeJSON(w, status, toParticipantJSON(view))
}

func (h *participantHandlers) delivery(w http.ResponseWriter, r *http.Request) {
	var body participantDeliveryJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	h.withTenant(w, r, func(tenantID ids.TenantID) (appparticipant.View, error) {
		return h.service.SetDelivery(r.Context(), tenantID, body.Delivery, body.Version)
	})
}

func subsidyValueResponse(subsidy participant.Subsidy) any {
	if subsidy.Type == participant.SubsidyFixed {
		return rubles(subsidy.Value)
	}
	return subsidy.Value
}
