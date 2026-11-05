package taxonomy

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

type dictionaryValue struct {
	canonical string
	order     int
}

type dictionaryMetadata struct {
	Order int `json:"order"`
}

// resolveDictionaries сортирует блокировки по коду, чтобы разные схемы не создавали взаимных ожиданий.
func resolveDictionaries(ctx context.Context, registry ports.TaxonomyDictionaries, d *taxonomy.Definition) error {
	codes := map[string]bool{}
	for _, a := range d.Attributes {
		if a.Dictionary != "" {
			codes[a.Dictionary] = true
		}
	}
	sorted := make([]string, 0, len(codes))
	for code := range codes {
		sorted = append(sorted, code)
	}
	sort.Strings(sorted)
	kinds := map[string]reference.Kind{}
	values := map[string][]string{}
	for _, code := range sorted {
		dictionary, err := registry.LockDictionary(ctx, code)
		if err != nil {
			var typed *errs.Error
			if errors.As(err, &typed) && typed.Code() == errs.NotFound {
				return taxonomy.ErrInvalid.WithMessage("словарь не существует: " + code)
			}
			return err
		}
		if dictionary == nil {
			return taxonomy.ErrInvalid.WithMessage("словарь не существует: " + code)
		}
		kinds[code] = dictionary.State().Kind
		if kinds[code] != reference.KindClosed {
			continue
		}
		entries, err := registry.AllEntries(ctx, code)
		if err != nil {
			return err
		}
		ordered := []dictionaryValue{}
		for _, entry := range entries {
			state := entry.State()
			if state.Status != reference.StatusConfirmed {
				continue
			}
			var metadata dictionaryMetadata
			if err := json.Unmarshal(state.Metadata, &metadata); err != nil {
				return taxonomy.ErrInvalid.WithMessage("неверный порядок значений словаря: " + code)
			}
			ordered = append(ordered, dictionaryValue{canonical: state.CanonicalName, order: metadata.Order})
		}
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].order != ordered[j].order {
				return ordered[i].order < ordered[j].order
			}
			return ordered[i].canonical < ordered[j].canonical
		})
		for _, value := range ordered {
			values[code] = append(values[code], value.canonical)
		}
	}
	// Копия не позволяет менять входную поставку через общий backing array.
	d.Attributes = append([]taxonomy.Attribute(nil), d.Attributes...)
	for i := range d.Attributes {
		a := &d.Attributes[i]
		a.Values = nil
		if a.Dictionary == "" {
			continue
		}
		expected := reference.KindOpen
		if a.Type == "enum" {
			expected = reference.KindClosed
		}
		if kinds[a.Dictionary] != expected {
			return taxonomy.ErrInvalid.WithMessage("неверный вид словаря: " + a.Dictionary)
		}
		if expected == reference.KindClosed {
			a.Values = values[a.Dictionary]
			if len(a.Values) == 0 {
				return taxonomy.ErrInvalid.WithMessage("нет подтверждённых значений: " + a.Dictionary)
			}
		}
	}
	return d.Validate()
}
