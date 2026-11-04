package fake

import (
	"context"
	"slices"
	"sync"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// ReferenceAPI — удалённый владелец реестра в тестах транспорта.
type ReferenceAPI struct {
	mu       sync.Mutex
	Requests []ports.ReferenceRequest
	Response ports.ReferenceResponse
	Err      error
}

// ReferenceValues — общий набор значений в тестах схем, вне fake.Store/транзакций PG.
type ReferenceValues struct {
	Items map[string][]ports.ReferenceValue
	Err   error
}

// Do запоминает запрос к владельцу, не заводя локальные записи.
func (f *ReferenceAPI) Do(_ context.Context, request ports.ReferenceRequest) (ports.ReferenceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, request)
	return f.Response, f.Err
}

// Calls — снимок запросов для проверки авторизации и контракта.
func (f *ReferenceAPI) Calls() []ports.ReferenceRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.Requests)
}

// Values возвращает независимую порцию: гидратация не меняет источник теста.
func (f *ReferenceValues) Values(_ context.Context, dictionary string) ([]ports.ReferenceValue, error) {
	return slices.Clone(f.Items[dictionary]), f.Err
}
