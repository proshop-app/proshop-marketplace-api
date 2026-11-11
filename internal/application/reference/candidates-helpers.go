package reference

import (
	"context"
	"encoding/json"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (s *Service) createCandidates(ctx context.Context, code string, items []ports.AttributeValue) (int, error) {
	eligible := make([]ports.AttributeValue, 0, len(items))
	for _, item := range items {
		if reference.CanonicalLanguageAllowed(code, item.Value) {
			eligible = append(eligible, item)
		}
	}
	items = eligible
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, reference.Key(item.Value))
	}
	var owners map[string]ids.UUID
	err := s.read(ctx, code, func(ctx context.Context, repo reference.Repository, _ *reference.Dictionary) error {
		var err error
		owners, err = repo.KeyOwners(ctx, code, keys)
		return err
	})
	if err != nil {
		return 0, err
	}
	created := 0
	for _, item := range items {
		key := reference.Key(item.Value)
		if _, taken := owners[key]; taken {
			continue
		}
		metadata, err := json.Marshal(candidateMetadata{Catalog: catalogObservation{
			Documents:  item.Documents,
			Sellers:    item.Sellers,
			ObservedAt: s.clock.Now().UTC().Format(time.RFC3339),
		}})
		if err != nil {
			return created, err
		}
		result, err := s.Create(ctx, CreateCommand{
			Dictionary:  code,
			Name:        item.Value,
			Status:      reference.StatusCandidate,
			Occurrences: item.Documents,
			Metadata:    metadata,
			Meta:        reference.Meta{Source: reference.SourceCatalog},
		})
		if err != nil {
			return created, err
		}
		owners[key] = result.Entry.ID
		if result.Created {
			created++
		}
	}
	return created, nil
}
