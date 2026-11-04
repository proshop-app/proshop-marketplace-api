package ports

import (
	"context"
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// ReferenceRequest — запрос к единственному владельцу общего реестра.
// Actor задаёт доверенный backend после проверки оператора, а не браузер.
type ReferenceRequest struct {
	Method     string
	Dictionary string
	Resource   string
	Query      string
	Body       json.RawMessage
	IfMatch    string
	Actor      string
	Write      bool
}

// ReferenceResponse сохраняет публичный контракт оператора без локальных мутаций.
type ReferenceResponse struct {
	Status int
	Body   json.RawMessage
	ETag   string
}

// ReferenceAPI — backend общего реестра MongoDB platform.
type ReferenceAPI interface {
	Do(context.Context, ReferenceRequest) (ReferenceResponse, error)
}

// ReferenceValue — подтверждённое значение закрытой схемы в порядке владельца.
type ReferenceValue struct {
	ID    string             `json:"id"`
	Value string             `json:"value"`
	Order int                `json:"order"`
	Meta  taxonomy.ValueMeta `json:"meta,omitempty"`
}

// ReferenceValues читает полный закрытый словарь. Ошибка не заменяется PG-снимком.
type ReferenceValues interface {
	Values(context.Context, string) ([]ReferenceValue, error)
}
