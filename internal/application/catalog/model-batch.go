package catalog

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

type modelBatchValues map[string]map[string]string

func (m *ModelKeys) forBatch(
	ctx context.Context,
	states []ports.OfferState,
	definition taxonomy.Definition,
) *ModelKeys {
	if m == nil || !m.enabled {
		return m
	}
	requested := modelBatchRequests(states, definition)
	values := modelBatchValues{}
	// Устойчивый порядок словарей упрощает диагностику и воспроизводимость отказов.
	dictionaries := make([]string, 0, len(requested))
	for dictionary := range requested {
		dictionaries = append(dictionaries, dictionary)
	}
	sort.Strings(dictionaries)
	for _, dictionary := range dictionaries {
		texts := make([]string, 0, len(requested[dictionary]))
		for text := range requested[dictionary] {
			texts = append(texts, text)
		}
		sort.Strings(texts)
		values[dictionary] = m.resolveBatch(ctx, dictionary, texts)
	}
	return &ModelKeys{resolver: values, observer: m.observer, enabled: true}
}

func modelBatchRequests(states []ports.OfferState, definition taxonomy.Definition) map[string]map[string]bool {
	requested := map[string]map[string]bool{}
	for _, state := range states {
		if state.Product == nil {
			continue
		}
		publication := state.Product.State().Card.Publication
		schema, _, _ := definition.Category(publication.CategoryID)
		if schema.Identity == nil {
			continue
		}
		codes := append([]string{}, schema.Identity.Model...)
		codes = append(codes, schema.Identity.Axes...)
		codes = append(codes, schema.Identity.Exact...)
		for _, field := range schema.Attributes {
			if field.Dictionary == "" || field.Type == "number" || field.Format == "gtin" || field.Format == "isbn" {
				continue
			}
			if !slices.Contains(codes, field.Code) {
				continue
			}
			var text string
			if json.Unmarshal(publication.Attributes[field.Code], &text) != nil || strings.TrimSpace(text) == "" {
				continue
			}
			if requested[field.Dictionary] == nil {
				requested[field.Dictionary] = map[string]bool{}
			}
			requested[field.Dictionary][text] = true
		}
	}
	return requested
}

func (m *ModelKeys) resolveBatch(ctx context.Context, dictionary string, texts []string) map[string]string {
	if resolver, ok := m.resolver.(ports.DictionaryBatchResolver); ok {
		attempt, cancel := context.WithTimeout(ctx, modelResolveTimeout)
		defer cancel()
		result, err := resolver.ResolveDictionaryBatch(attempt, dictionary, texts)
		if err != nil {
			if m.observer != nil {
				m.observer.ResolveFailed()
			}
			return nil
		}
		return result
	}
	result := map[string]string{}
	for _, text := range texts {
		result[text] = m.resolve(ctx, dictionary, text)
	}
	return result
}

func (v modelBatchValues) Resolve(_ context.Context, text string) (string, bool, error) {
	id := v["brand"][text]
	return id, id != "", nil
}

func (v modelBatchValues) ResolveDictionary(_ context.Context, dictionary, text string) (string, bool, error) {
	id := v[dictionary][text]
	return id, id != "", nil
}
