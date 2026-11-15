package delivery

import (
	"math"
	"strings"
	"unicode/utf8"
)

const (
	maxCodeLength = 128
	maxTextLength = 1000
)

// Location — координаты в градусах WGS 84.
type Location struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// PickupPoint — пункт выдачи перевозчика из ответа магазина.
type PickupPoint struct {
	Carrier    Carrier  `json:"carrier"`
	Code       string   `json:"code"`
	Name       string   `json:"name"`
	Address    string   `json:"address"`
	City       string   `json:"city"`
	RegionCode string   `json:"regionCode"`
	Location   Location `json:"location"`
	// Schedule — график работы словами перевозчика.
	Schedule string `json:"schedule"`
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
) (PickupPoint, error) {
	point := PickupPoint{
		Carrier:  carrier,
		Code:     strings.TrimSpace(code),
		Name:     strings.TrimSpace(name),
		Address:  strings.TrimSpace(address),
		City:     strings.TrimSpace(city),
		Location: location,
		Schedule: strings.TrimSpace(schedule),
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

// NormalizeCity определяет общий ключ кэша без изменения регистра запроса магазина.
func NormalizeCity(city string) (string, error) {
	city = strings.Join(strings.Fields(city), " ")
	if city == "" || utf8.RuneCountInString(city) > 120 {
		return "", ErrInvalidCity
	}
	return city, nil
}
