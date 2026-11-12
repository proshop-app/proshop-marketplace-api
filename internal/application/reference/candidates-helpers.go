package reference

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (s *Service) createCandidates(
	ctx context.Context,
	code string,
	scope taxonomy.CandidateScope,
	items []ports.AttributeValue,
) (int, error) {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, reference.Key(item.Value))
	}
	if len(keys) == 0 {
		return 0, nil
	}
	created := 0
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		// Общий порядок блокировок защищает проверку всего набора от гонок и взаимных блокировок.
		sources := slices.Clone(scope.Dictionaries)
		slices.Sort(sources)
		occupied := map[string]bool{}
		var target *reference.Dictionary
		for _, source := range sources {
			dict, err := tx.Reference().LockDictionary(ctx, source)
			if err != nil {
				return err
			}
			if err := dict.CheckNormalizer(); err != nil {
				return err
			}
			if source == code {
				target = dict
			}
			owners, err := tx.Reference().KeyOwners(ctx, source, keys)
			if err != nil {
				return err
			}
			for key := range owners {
				occupied[key] = true
			}
		}
		if target == nil || target.State().Kind != reference.KindOpen {
			return nil
		}
		_, err := s.mutateLockedDictionary(ctx, tx, target, func(ctx context.Context, m *session) error {
			draft := reference.NewDraft(code)
			var primary ids.UUID
			for _, item := range items {
				key := reference.Key(item.Value)
				if occupied[key] || !reference.CanonicalLanguageAllowed(code, item.Value) {
					continue
				}
				metadata, err := json.Marshal(candidateMetadata{Catalog: catalogObservation{
					Attribute:  scope.Attribute,
					Categories: append([]string{}, scope.Categories...),
					Documents:  item.Documents,
					Sellers:    item.Sellers,
					ObservedAt: m.now.UTC().Format(time.RFC3339),
				}})
				if err != nil {
					return err
				}
				input := reference.NewEntryInput{
					ID:               s.ids.NewUUID(),
					CanonicalAliasID: s.ids.NewUUID(),
					Name:             item.Value,
					Status:           reference.StatusCandidate,
					Source:           reference.SourceCatalog,
					Occurrences:      item.Documents,
					Metadata:         metadata,
				}
				entry, err := reference.NewEntry(m.dict, input, m.now)
				if err != nil {
					return err
				}
				draft.Track(entry)
				if primary == "" {
					primary = entry.ID()
				}
				occupied[key] = true
				created++
			}
			meta := reference.Meta{Source: reference.SourceCatalog}
			_, err := m.record(ctx, draft, reference.ChangeCreated, primary, meta, 0)
			return err
		})
		return err
	})
	if err != nil {
		return 0, err
	}
	return created, nil
}
