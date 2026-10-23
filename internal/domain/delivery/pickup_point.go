package delivery

import (
	"encoding/json"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxCodeLength = 128
	maxTextLength = 1000
)

// Location — координаты в градусах WGS 84.
type Location struct {
	Lat float64
	Lon float64
}

// PickupPoint — пункт выдачи перевозчика в справочнике маркетплейса.
type PickupPoint struct {
	Carrier  Carrier
	Code     string
	Name     string
	Address  string
	City     string
	Location Location
	// Schedule — график работы словами перевозчика.
	Schedule string
	// Payload — исходная запись перевозчика: для разбора спорных случаев.
	Payload     json.RawMessage
	Active      bool
	RefreshedAt time.Time
}

// Area — где искать пункты: область карты или город.
type Area struct {
	// BBox — область карты: юго-западный и северо-восточный углы.
	BBox *BBox
	// City — город словами перевозчика.
	City string
}

// BBox — прямоугольник карты.
type BBox struct {
	MinLon, MinLat, MaxLon, MaxLat float64
}

// Valid — координаты в допустимых пределах.
func (l Location) Valid() bool {
	return !math.IsNaN(l.Lat) && !math.IsNaN(l.Lon) && l.Lat >= -90 && l.Lat <= 90 && l.Lon >= -180 && l.Lon <= 180
}

// NewPickupPoint проверяет запись перевозчика.
func NewPickupPoint(
	carrier Carrier,
	code, name, address, city string,
	location Location,
	schedule string,
	payload json.RawMessage,
	now time.Time,
) (PickupPoint, error) {
	point := PickupPoint{
		Carrier:     carrier,
		Code:        strings.TrimSpace(code),
		Name:        strings.TrimSpace(name),
		Address:     strings.TrimSpace(address),
		City:        strings.TrimSpace(city),
		Location:    location,
		Schedule:    strings.TrimSpace(schedule),
		Payload:     payload,
		Active:      true,
		RefreshedAt: now,
	}

	switch {
	case point.Code == "" || len(point.Code) > maxCodeLength:
		return PickupPoint{}, ErrInvalidPickupPoint.WithMessage("у пункта нет кода")
	case point.Address == "" || utf8.RuneCountInString(point.Address) > maxTextLength:
		return PickupPoint{}, ErrInvalidPickupPoint.WithMessage("у пункта нет адреса")
	case !location.Valid() || (location.Lat == 0 && location.Lon == 0):
		return PickupPoint{}, ErrInvalidPickupPoint.WithMessage("у пункта нет координат")
	}

	if point.Name == "" {
		point.Name = point.Address
	}

	return point, nil
}

// Validate проверяет область поиска: ровно одно из двух.
func (a Area) Validate() error {
	city := strings.TrimSpace(a.City)

	switch {
	case a.BBox == nil && city == "":
		return ErrInvalidArea
	case a.BBox != nil && city != "":
		return ErrInvalidArea.WithMessage("задайте что-то одно: область карты или город")
	case a.BBox != nil:
		box := a.BBox
		southWest, northEast := Location{Lat: box.MinLat, Lon: box.MinLon}, Location{Lat: box.MaxLat, Lon: box.MaxLon}
		if !southWest.Valid() || !northEast.Valid() || box.MinLat > box.MaxLat || box.MinLon > box.MaxLon {
			return ErrInvalidArea.WithMessage("область карты: minLon,minLat,maxLon,maxLat в градусах")
		}
	case utf8.RuneCountInString(city) > 200:
		return ErrInvalidArea.WithMessage("слишком длинное название города")
	}

	return nil
}
