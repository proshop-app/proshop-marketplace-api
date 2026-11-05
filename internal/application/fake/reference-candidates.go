package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// ReferenceCandidates хранит страницы по словарю и курсору для тестов сборщика.
type ReferenceCandidates struct {
	Pages  map[string]map[string]ports.AttributeValuesPage
	Errors map[string]error
	Calls  []string
}

// AttributeValues возвращает заданную страницу и запоминает обращение.
func (f *ReferenceCandidates) AttributeValues(
	_ context.Context,
	code string,
	_ int,
	after string,
) (ports.AttributeValuesPage, error) {
	f.Calls = append(f.Calls, code+":"+after)
	return f.Pages[code][after], f.Errors[code]
}
