package ports

import "context"

// DetectedCity — подсказка по IP; пустой город требует ручного выбора.
type DetectedCity struct {
	City    string `json:"city"`
	Country string `json:"country"`
}

// CitySuggestion — тот же справочник назначения, что у витрины магазина.
type CitySuggestion struct {
	Label   string `json:"label"`
	City    string `json:"city"`
	Region  string `json:"region,omitempty"`
	FiasID  string `json:"fiasId,omitempty"`
	KladrID string `json:"kladrId,omitempty"`
}

// CityDetector определяет город по адресу клиента.
type CityDetector interface {
	DetectCity(context.Context, string) (DetectedCity, error)
}

// CitySuggestions — общий справочник городов сервера платформы.
type CitySuggestions interface {
	SuggestCities(context.Context, string) ([]CitySuggestion, error)
}
