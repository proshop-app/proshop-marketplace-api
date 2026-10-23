package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

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
	RegionCode  string       `json:"regionCode"`
	Carrier     string       `json:"carrier"`
	Code        string       `json:"code"`
	Name        string       `json:"name"`
	Address     string       `json:"address"`
	City        string       `json:"city"`
	Location    locationJSON `json:"location"`
	Schedule    string       `json:"schedule"`
	RefreshedAt time.Time    `json:"refreshedAt"`
}

type deliveryHandlers struct {
	directory *appdelivery.Directory
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

// pickupPoints — пункты перевозчика: ?carrier=&bbox=minLon,minLat,maxLon,maxLat | city=&limit=
func (h *deliveryHandlers) pickupPoints(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()

	carrier, err := delivery.ParseCarrier(values.Get("carrier"))
	if err != nil {
		writeError(w, r, err)

		return
	}

	area := delivery.Area{City: values.Get("city")}

	if raw := values.Get("bbox"); raw != "" {
		box, err := parseBBox(raw)
		if err != nil {
			writeError(w, r, err)

			return
		}

		area.BBox = &box
	}

	limit := 0
	if raw := values.Get("limit"); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil {
			writeError(w, r, ErrInvalidQuery.WithMessage("limit — целое число"))

			return
		}
	}

	points, err := h.directory.Search(r.Context(), carrier, area, limit)
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
			Schedule:    p.Schedule,
			RefreshedAt: p.RefreshedAt.UTC(),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": body})
}

func parseBBox(raw string) (delivery.BBox, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != 4 {
		return delivery.BBox{}, delivery.ErrInvalidArea.WithMessage("bbox — minLon,minLat,maxLon,maxLat")
	}

	values := make([]float64, 4)
	for i, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return delivery.BBox{}, delivery.ErrInvalidArea.WithMessage("bbox — четыре числа через запятую")
		}

		values[i] = value
	}

	return delivery.BBox{MinLon: values[0], MinLat: values[1], MaxLon: values[2], MaxLat: values[3]}, nil
}
