package buyer

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ipnet"
)

// ErrCityQuery — недопустимый поисковый запрос города.
var ErrCityQuery = errs.New(errs.Invalid, "buyer.city_query", "введите название города от 2 до 120 символов")

// Cities — общесайтовая подсказка города, независимая от входа и состава покупки.
type Cities struct {
	detector    ports.CityDetector
	suggestions ports.CitySuggestions
	limiter     ports.RateLimiter
}

// NewCities собирает определение города и общий справочник подсказок.
func NewCities(detector ports.CityDetector, suggestions ports.CitySuggestions, limiter ports.RateLimiter) *Cities {
	return &Cities{detector: detector, suggestions: suggestions, limiter: limiter}
}

// Detect определяет город по адресу доверенного клиента.
func (s *Cities) Detect(ctx context.Context, ip string) (ports.DetectedCity, error) {
	if err := s.allow(ctx, ip); err != nil {
		return ports.DetectedCity{}, err
	}
	return s.detector.DetectCity(ctx, ip)
}

// Suggest ищет города тем же источником, что витрина магазина.
func (s *Cities) Suggest(ctx context.Context, ip, value string) ([]ports.CitySuggestion, error) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) < 2 || utf8.RuneCountInString(value) > 120 {
		return nil, ErrCityQuery
	}
	if err := s.allow(ctx, ip); err != nil {
		return nil, err
	}
	return s.suggestions.SuggestCities(ctx, value)
}

func (s *Cities) allow(ctx context.Context, ip string) error {
	allowed, retry, err := s.limiter.Allow(ctx, "city:"+ipnet.Network(ip), regionRequestLimit, regionRequestWindow)
	if err != nil {
		return err
	}
	if !allowed {
		return &RegionLimitError{RetryAfter: retry}
	}
	return nil
}
