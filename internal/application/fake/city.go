package fake

import (
	"context"
	"sync"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// Cities — управляемые ответы определения и подсказок города.
type Cities struct {
	mu          sync.Mutex
	Detection   ports.DetectedCity
	Suggestions []ports.CitySuggestion
	Error       error
	IPs         []string
	Values      []string
}

// DetectCity сохраняет адрес вызова и отдаёт настроенный ответ.
func (f *Cities) DetectCity(_ context.Context, ip string) (ports.DetectedCity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.IPs = append(f.IPs, ip)
	return f.Detection, f.Error
}

// SuggestCities сохраняет запрос и отдаёт настроенные подсказки.
func (f *Cities) SuggestCities(_ context.Context, value string) ([]ports.CitySuggestion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Values = append(f.Values, value)
	return f.Suggestions, f.Error
}
