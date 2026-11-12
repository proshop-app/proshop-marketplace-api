package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// ReferenceCandidates хранит страницы по атрибуту и курсору для тестов сборщика.
type ReferenceCandidates struct {
	Pages   map[string]map[string]ports.AttributeValuesPage
	Errors  map[string]error
	Calls   []string
	Queries []ports.AttributeValuesQuery
	Respond func(ports.AttributeValuesQuery) (ports.AttributeValuesPage, error)
}

// AttributeValues возвращает заданную страницу и запоминает обращение.
func (f *ReferenceCandidates) AttributeValues(
	_ context.Context,
	query ports.AttributeValuesQuery,
) (ports.AttributeValuesPage, error) {
	f.Calls = append(f.Calls, query.Attribute+":"+query.After)
	f.Queries = append(f.Queries, query)
	if f.Respond != nil {
		return f.Respond(query)
	}
	return f.Pages[query.Attribute][query.After], f.Errors[query.Attribute]
}
