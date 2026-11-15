package http

import (
	"net/http"

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

	writeJSON(w, http.StatusOK, map[string]any{"items": body})
}
