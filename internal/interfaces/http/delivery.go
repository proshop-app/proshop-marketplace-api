package http

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

type carrierJSON struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type locationJSON struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type pickupPointJSON struct {
	RegionCode string       `json:"regionCode"`
	Carrier    string       `json:"carrier"`
	Code       string       `json:"code"`
	Name       string       `json:"name"`
	Address    string       `json:"address"`
	City       string       `json:"city"`
	Location   locationJSON `json:"location"`
	Schedule   string       `json:"schedule"`
}

type deliveryHandlers struct {
	directory *appdelivery.Service
	checkouts *appcheckout.Service
}

// carriers — подключённые перевозчики для переключателя карты.
func (h *deliveryHandlers) carriers(w http.ResponseWriter, _ *http.Request) {
	carriers := h.directory.Carriers()
	body := make([]carrierJSON, 0, len(carriers))

	for _, carrier := range carriers {
		body = append(body, carrierJSON{Code: string(carrier.Code), Name: carrier.Name})
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": body})
}

// pickupPoints — пункты перевозчика: ?carrier=&city=
func (h *deliveryHandlers) pickupPoints(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()

	carrier, err := delivery.ParseCarrier(values.Get("carrier"))
	if err != nil {
		writeError(w, r, err)

		return
	}

	if values.Has("bbox") || values.Has("limit") {
		writeError(w, r, ErrInvalidQuery.WithMessage("bbox и limit больше не поддерживаются"))
		return
	}
	points, err := h.directory.Search(r.Context(), carrier, values.Get("city"))
	if err != nil {
		writeError(w, r, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": pickupResponse(points)})
}

func pickupResponse(points []delivery.PickupPoint) []pickupPointJSON {
	body := make([]pickupPointJSON, 0, len(points))
	for _, p := range points {
		body = append(body, pickupPointJSON{
			Carrier:    string(p.Carrier),
			Code:       p.Code,
			Name:       p.Name,
			Address:    p.Address,
			City:       p.City,
			RegionCode: p.RegionCode,
			Location: locationJSON{
				Lat: p.Location.Lat,
				Lon: p.Location.Lon,
			},
			Schedule: p.Schedule,
		})
	}

	return body
}

// checkoutPickupPoints не читает реестр всех магазинов и ограничивает размер ответа карте.
func (h *deliveryHandlers) checkoutPickupPoints(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseUUID(r.PathValue("checkoutId"))
	if err != nil {
		writeError(w, r, checkout.ErrNotFound)
		return
	}
	state, err := h.checkouts.Get(r.Context(), BuyerID(r.Context()), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	values := r.URL.Query()
	carrier, err := delivery.ParseCarrier(values.Get("carrier"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	offset, limit := 0, 100
	if values.Has("offset") {
		offset, err = strconv.Atoi(values.Get("offset"))
		if err != nil || offset < 0 {
			writeError(w, r, ErrInvalidQuery)
			return
		}
	}
	if values.Has("limit") {
		limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil || limit < 1 || limit > 200 {
			writeError(w, r, ErrInvalidQuery)
			return
		}
	}
	var box *delivery.Bounds
	if values.Has("bbox") {
		parsed, err := parsePickupBounds(values.Get("bbox"))
		if err != nil {
			writeError(w, r, err)
			return
		}
		box = &parsed
		offset, limit = 0, delivery.PickupMapLimit
	}
	groups := state.Groups
	if tenant := values.Get("tenantId"); tenant != "" {
		groups = slices.DeleteFunc(slices.Clone(groups), func(group checkout.Group) bool {
			return string(group.TenantID) != tenant
		})
		if len(groups) == 0 {
			writeError(w, r, checkout.ErrNotFound)
			return
		}
	}
	tenants := make([]ids.TenantID, 0, len(groups))
	regions := []string{}
	for _, group := range groups {
		tenants = append(tenants, group.TenantID)
		if slices.Contains(group.Carriers, string(carrier)) {
			regions = append(regions, group.Regions...)
		}
	}
	result, err := h.directory.QueryFor(r.Context(), carrier, values.Get("city"), tenants, regions, box, offset, limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":      pickupResponse(result.Items),
		"total":      result.Total,
		"refineArea": result.Refine,
		"bounds":     result.Bounds,
		"offset":     offset,
		"limit":      limit,
	})
}

func parsePickupBounds(raw string) (delivery.Bounds, error) {
	var bounds delivery.Bounds
	parts := strings.Split(raw, ",")
	if len(parts) != len(bounds) {
		return bounds, ErrInvalidQuery
	}
	for index, value := range parts {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return bounds, ErrInvalidQuery
		}
		bounds[index] = parsed
	}
	if !bounds.Valid() {
		return bounds, ErrInvalidQuery
	}
	return bounds, nil
}
