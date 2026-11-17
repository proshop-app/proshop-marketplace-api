package http

import (
	"net/http"
	"slices"
	"strconv"

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
	tenants := make([]ids.TenantID, 0, len(state.Groups))
	for _, g := range state.Groups {
		tenants = append(tenants, g.TenantID)
	}
	points, err := h.directory.SearchFor(r.Context(), carrier, values.Get("city"), tenants)
	if err != nil {
		writeError(w, r, err)
		return
	}
	// Справочник общий, но покупатель видит только регионы магазинов своей покупки.
	filtered := make([]delivery.PickupPoint, 0, len(points))
	for _, point := range points {
		if slices.ContainsFunc(state.Groups, func(g checkout.Group) bool {
			return slices.Contains(g.Carriers, string(carrier)) && slices.Contains(g.Regions, point.RegionCode)
		}) {
			filtered = append(filtered, point)
		}
	}
	start := min(offset, len(filtered))
	end := start + min(limit, len(filtered)-start)
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  pickupResponse(filtered[start:end]),
		"total":  len(filtered),
		"offset": offset,
		"limit":  limit,
	})
}
