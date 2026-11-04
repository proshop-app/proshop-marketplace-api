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
// Dictionaries — общий список владельца для сверки копии; счётчики — число обращений.
type ReferenceValues struct {
	mu            sync.Mutex
	Items         map[string][]ports.ReferenceValue
	Dictionaries  []ports.ReferenceRevision
	Err           error
	ValueCalls    int
	RevisionCalls int
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
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ValueCalls++
	if f.Err != nil {
		return nil, f.Err
	}
	return slices.Clone(f.Items[dictionary]), nil
}

// Revisions — общий список словарей владельца; Err роняет и его.
func (f *ReferenceValues) Revisions(context.Context) ([]ports.ReferenceRevision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RevisionCalls++
	if f.Err != nil {
		return nil, f.Err
	}
	return slices.Clone(f.Dictionaries), nil
}
