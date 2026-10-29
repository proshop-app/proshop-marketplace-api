package catalog

import (
	"context"
	"encoding/json"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
)

const modelResolveTimeout = 300 * time.Millisecond

// ModelKeys вычисляет ключ модели в режиме наблюдения.
type ModelKeys struct {
	resolver ports.BrandResolver
	observer ports.ModelKeyObserver
	enabled  bool
}

// NewModelKeys создаёт вычислитель ключей модели.
func NewModelKeys(resolver ports.BrandResolver, observer ports.ModelKeyObserver, enabled bool) *ModelKeys {
	return &ModelKeys{
		resolver: resolver,
		observer: observer,
		enabled:  enabled,
	}
}

// Apply пересчитывает ключ, не меняя публикацию товара.
func (m *ModelKeys) Apply(ctx context.Context, product *catalog.Product) bool {
	key, source := m.calculate(ctx, product.State().Card.Publication.Attributes)

	return product.SetModelKey(key, source)
}

func (m *ModelKeys) calculate(ctx context.Context, attributes map[string]json.RawMessage) (string, string) {
	if m == nil || !m.enabled {
		return "", ""
	}
	if gtin, valid := catalog.ParseGTIN(modelAttribute(attributes, "gtin")); valid {
		return "gtin:" + gtin, "gtin"
	}
	part := catalog.ModelPartKey(modelAttribute(attributes, "part_number"))
	brand := modelAttribute(attributes, "brand")
	if part == "" || brand == "" || m.resolver == nil {
		return "", ""
	}
	attempt, cancel := context.WithTimeout(ctx, modelResolveTimeout)
	defer cancel()
	id, found, err := m.resolver.Resolve(attempt, brand)
	if err != nil {
		if m.observer != nil {
			m.observer.ResolveFailed()
		}
		return "", ""
	}
	if !found || id == "" {
		return "", ""
	}

	return "mpn:" + id + ":" + part, "mpn"
}

func modelAttribute(attributes map[string]json.RawMessage, code string) string {
	var value string
	if json.Unmarshal(attributes[code], &value) != nil {
		return ""
	}

	return value
}
