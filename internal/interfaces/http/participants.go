package http

import (
	"net/http"
	"strconv"
	"time"

	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// participantJSON — участник в операторском ответе.
type participantJSON struct {
	Trust             string                       `json:"trust"`
	ApprovedOffers    int                          `json:"approvedOffers"`
	TenantID          string                       `json:"tenantId"`
	Slug              string                       `json:"slug"`
	Name              string                       `json:"name"`
	Status            string                       `json:"status"`
	Enabled           bool                         `json:"enabled"`
	ShopEnabled       bool                         `json:"shopEnabled"`
	Subsidy           subsidyJSON                  `json:"subsidy"`
	FreeDeliveryFrom  int64                        `json:"freeDeliveryFrom"`
	Delivery          participant.DeliverySettings `json:"delivery"`
	SettingsSyncedAt  *time.Time                   `json:"settingsSyncedAt"`
	TenantStatus      string                       `json:"tenantStatus"`
	Visible           bool                         `json:"visible"`
	CheckoutAvailable bool                         `json:"checkoutAvailable"`
	Version           int64                        `json:"version"`
	CreatedAt         time.Time                    `json:"createdAt"`
	UpdatedAt         time.Time                    `json:"updatedAt"`
}

// subsidyJSON — субсидия доставки продавца: value — копейки для fixed, процент для percent.
type subsidyJSON struct {
	Enabled   bool   `json:"enabled"`
	Type      string `json:"type,omitempty"`
	Value     int64  `json:"value"`
	MaxAmount int64  `json:"maxAmount"`
}

type participantListJSON struct {
	Items  []participantJSON `json:"items"`
	Total  int               `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

type participantHandlers struct {
	service *appparticipant.Service
}

type createParticipantJSON struct {
	TenantID string `json:"tenantId"`
	Name     string `json:"name"`
}

type renameParticipantJSON struct {
	Name string `json:"name"`
}

type participantDeliveryJSON struct {
	Version  int64                        `json:"version"`
	Delivery participant.DeliverySettings `json:"delivery"`
}

func toParticipantJSON(view appparticipant.View) participantJSON {
	p := view.Participant
	out := participantJSON{
		Trust:             p.Trust,
		ApprovedOffers:    p.ApprovedOffers,
		TenantID:          string(p.TenantID),
		FreeDeliveryFrom:  p.FreeDeliveryFrom.Kopecks(),
		Delivery:          p.Delivery,
		Slug:              p.Slug,
		Name:              p.Name,
		Status:            string(p.Status),
		Enabled:           p.Enabled,
		TenantStatus:      string(view.TenantStatus),
		Visible:           view.Visibility.Visible,
		CheckoutAvailable: view.Visibility.CheckoutAvailable,
		Version:           p.Version,
		CreatedAt:         p.CreatedAt.UTC(),
		UpdatedAt:         p.UpdatedAt.UTC(),
		ShopEnabled:       p.ShopEnabled,
		Subsidy: subsidyJSON{
			Enabled:   p.Subsidy.Enabled,
			Type:      string(p.Subsidy.Type),
			Value:     p.Subsidy.Value,
			MaxAmount: p.Subsidy.MaxAmount.Kopecks(),
		},
	}
	if !p.SettingsSyncedAt.IsZero() {
		synced := p.SettingsSyncedAt.UTC()
		out.SettingsSyncedAt = &synced
	}

	return out
}

func (h *participantHandlers) list(w http.ResponseWriter, r *http.Request) {
	page := participant.Page{Limit: appparticipant.MaxPageLimit, Trust: r.URL.Query().Get("trust")}

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

	view, err := h.service.Create(r.Context(), appparticipant.CreateCommand{TenantID: tenantID, Name: body.Name})
	respond(w, r, http.StatusCreated, view, err)
}

func (h *participantHandlers) get(w http.ResponseWriter, r *http.Request) {
	h.withTenant(w, r, func(tenantID ids.TenantID) (appparticipant.View, error) {
		return h.service.Get(r.Context(), tenantID)
	})
}

func (h *participantHandlers) rename(w http.ResponseWriter, r *http.Request) {
	var body renameParticipantJSON
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)

		return
	}

	h.withTenant(w, r, func(tenantID ids.TenantID) (appparticipant.View, error) {
		return h.service.Rename(r.Context(), tenantID, body.Name)
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
