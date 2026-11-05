package ports

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

// TaxonomyDictionaries фиксирует значения вместе со снимком; блокировка защищает от правок реестра.
type TaxonomyDictionaries interface {
	LockDictionary(context.Context, string) (*reference.Dictionary, error)
	AllEntries(context.Context, string) ([]*reference.Entry, error)
}
