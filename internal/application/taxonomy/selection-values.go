package taxonomy

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func loadSelectionValues(
	ctx context.Context,
	registry ports.TaxonomyDictionaries,
	code string,
) (taxonomy.DictionaryValues, error) {
	entries, err := registry.ConfirmedEntries(ctx, code)
	if err != nil {
		return taxonomy.DictionaryValues{}, err
	}
	metadata := make(map[string]taxonomy.ValueMeta)
	var terms map[string][]string
	for _, entry := range entries {
		state := entry.State()
		var meta taxonomy.ValueMeta
		if err := json.Unmarshal(state.Metadata, &meta); err != nil {
			return taxonomy.DictionaryValues{}, taxonomy.ErrInvalid.WithMessage("неверные метаданные словаря: " + code)
		}
		if err := meta.Validate(state.CanonicalName); err != nil {
			message := "неверные метаданные: " + code + "/" + state.CanonicalName
			return taxonomy.DictionaryValues{}, taxonomy.ErrInvalid.WithMessage(message).Wrap(err)
		}
		metadata[state.CanonicalName] = meta
		if expressions := selectionSearchTerms(code, state, meta.SecondaryLabel); len(expressions) > 0 {
			if terms == nil {
				terms = map[string][]string{}
			}
			terms[state.CanonicalName] = expressions
		}
	}
	// При поэтапной модерации диапазон начинает расширять фасет, когда подтверждены все его одиночные значения.
	source := maps.Clone(metadata)
	for canonical, meta := range source {
		if !confirmedCover(meta.Covers, source) {
			meta.Covers = nil
			metadata[canonical] = meta
		}
	}
	return taxonomy.DictionaryValues{ValuesMeta: metadata, SearchTermsByValue: terms}, nil
}

func confirmedCover(covers []string, values map[string]taxonomy.ValueMeta) bool {
	for _, covered := range covers {
		if meta, found := values[covered]; !found || len(meta.Covers) > 0 {
			return false
		}
	}
	return true
}

func selectionSearchTerms(dictionary string, entry reference.EntryState, label string) []string {
	aliases := slices.Clone(entry.Aliases)
	slices.SortFunc(aliases, func(a, b reference.AliasState) int {
		if a.Weight != b.Weight {
			return int(b.Weight) - int(a.Weight)
		}
		return strings.Compare(a.Text, b.Text)
	})
	candidates := make([]string, 0, 1+len(aliases))
	if label != "" {
		candidates = append(candidates, label)
	}
	for _, alias := range aliases {
		candidates = append(candidates, alias.Text)
	}
	seen := map[string]bool{taxonomy.SearchTermKey(entry.CanonicalName): true}
	var terms []string
	dropped := 0
	for _, candidate := range candidates {
		term := reference.Collapse(candidate)
		key := taxonomy.SearchTermKey(term)
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(terms) == taxonomy.MaxValueSearchTerms || taxonomy.ValidateSearchTerms([]string{term}) != nil {
			dropped++
			continue
		}
		terms = append(terms, term)
	}
	if dropped > 0 {
		slog.Warn(
			"поисковые выражения справочника отброшены",
			"dictionary",
			dictionary,
			"value",
			entry.CanonicalName,
			"dropped",
			dropped,
		)
	}
	return terms
}
