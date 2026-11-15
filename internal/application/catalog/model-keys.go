package catalog

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

const modelResolveTimeout = 300 * time.Millisecond

// ModelKeys вычисляет идентичность по схеме в режиме наблюдения.
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

// Apply пересчитывает ключи, не меняя публикацию товара.
func (m *ModelKeys) Apply(ctx context.Context, product *catalog.Product, definition taxonomy.Definition) bool {
	publication := product.State().Card.Publication
	schema, _, _ := definition.Category(publication.CategoryID)
	model, variant := m.calculate(ctx, schema, publication.Attributes)
	source := ""
	if model != "" {
		source = "identity"
	}
	return product.SetIdentityKeys(model, source, variant)
}

func (m *ModelKeys) calculate(
	ctx context.Context,
	schema taxonomy.CategorySchema,
	attributes map[string]json.RawMessage,
) (string, string) {
	if m == nil || !m.enabled || schema.Identity == nil {
		return "", ""
	}
	fields := make(map[string]taxonomy.Attribute, len(schema.Attributes))
	for _, field := range schema.Attributes {
		fields[field.Code] = field.Attribute
	}
	model := m.values(ctx, schema.Identity.Model, fields, attributes)
	if model == "" {
		return "", ""
	}
	axes := m.values(ctx, schema.Identity.Axes, fields, attributes)
	if len(schema.Identity.Axes) > 0 && axes == "" {
		return "", ""
	}
	key := schema.Schema + ":" + model
	variant := key
	if axes != "" {
		variant += ":" + axes
	}
	if exact := m.values(ctx, schema.Identity.Exact, fields, attributes); exact != "" {
		variant = "exact:" + exact
	}
	return key, variant
}

func (m *ModelKeys) values(
	ctx context.Context,
	codes []string,
	fields map[string]taxonomy.Attribute,
	attributes map[string]json.RawMessage,
) string {
	values := make([]string, 0, len(codes))
	for _, code := range codes {
		value := m.normalize(ctx, fields[code], attributes[code])
		if value == "" {
			return ""
		}
		// Экранирование разделителя исключает совпадение разных наборов значений.
		values = append(values, strings.NewReplacer("%", "%25", ":", "%3A").Replace(value))
	}
	return strings.Join(values, ":")
}

func (m *ModelKeys) normalize(ctx context.Context, field taxonomy.Attribute, raw json.RawMessage) string {
	if field.Type == "number" {
		return catalog.IdentityNumber(string(raw))
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return ""
	}
	switch field.Format {
	case "gtin":
		result, ok := catalog.ParseGTIN(value)
		if !ok || (len(result) == 14 && result[0] != '0') {
			return ""
		}
		return result[len(result)-13:]
	case "isbn":
		result, _ := catalog.ParseISBN(value)
		return result
	}
	if field.Dictionary != "" {
		return m.resolve(ctx, field.Dictionary, value)
	}
	return catalog.ModelPartKey(value)
}

func (m *ModelKeys) resolve(ctx context.Context, dictionary, value string) string {
	if m.resolver == nil {
		return ""
	}
	attempt, cancel := context.WithTimeout(ctx, modelResolveTimeout)
	defer cancel()
	var id string
	var found bool
	var err error
	if resolver, ok := m.resolver.(ports.DictionaryResolver); ok {
		id, found, err = resolver.ResolveDictionary(attempt, dictionary, value)
	} else if dictionary == "brand" {
		id, found, err = m.resolver.Resolve(attempt, value)
	}
	if err != nil && m.observer != nil {
		m.observer.ResolveFailed()
	}
	if err != nil || !found {
		return ""
	}
	return id
}
