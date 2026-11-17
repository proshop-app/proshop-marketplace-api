package http

import (
	"net/http"
	"strconv"
	"strings"

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
	query, err := pickupQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := h.directory.Query(r.Context(), query)
	writePickupArea(w, r, query, result, err)
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
	query, err := pickupQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := h.directory.CheckoutPoints(
		r.Context(),
		BuyerID(r.Context()),
		id,
		ids.TenantID(r.URL.Query().Get("tenantId")),
		query,
	)
	writePickupArea(w, r, query, result, err)
}

func pickupQuery(r *http.Request) (appdelivery.PickupQuery, error) {
	values := r.URL.Query()
	carrier, err := delivery.ParseCarrier(values.Get("carrier"))
	query := appdelivery.PickupQuery{Carrier: carrier, City: values.Get("city"), Limit: 100}
	if err != nil {
		return query, err
	}
	if values.Has("offset") {
		query.Offset, err = strconv.Atoi(values.Get("offset"))
		if err != nil || query.Offset < 0 {
			return query, ErrInvalidQuery
		}
	}
	if values.Has("limit") {
		query.Limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil || query.Limit < 1 || query.Limit > 200 {
			return query, ErrInvalidQuery
		}
	}
	if values.Has("bbox") {
		bounds, err := parsePickupBounds(values.Get("bbox"))
		if err != nil {
			return query, err
		}
		query.Bounds = &bounds
		query.Offset, query.Limit = 0, delivery.PickupMapLimit
	}
	return query, nil
}

func writePickupArea(
	w http.ResponseWriter,
	r *http.Request,
	query appdelivery.PickupQuery,
	result delivery.PickupArea,
	err error,
) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(
		w,
		http.StatusOK,
		map[string]any{
			"items":      pickupResponse(result.Items),
			"total":      result.Total,
			"refineArea": result.Refine,
			"bounds":     result.Bounds,
			"offset":     query.Offset,
			"limit":      query.Limit,
		},
	)
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
