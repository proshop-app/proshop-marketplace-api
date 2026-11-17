package reference

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ResolveBatch возвращает только точные разрешимые совпадения: кандидаты не нужны ключам модели.
func (s *Service) ResolveBatch(ctx context.Context, code string, texts []string) (map[string]ids.UUID, error) {
	result := map[string]ids.UUID{}
	err := s.read(ctx, code, func(ctx context.Context, repo reference.Repository, dict *reference.Dictionary) error {
		if err := dict.CheckNormalizer(); err != nil {
			return err
		}
		keys := map[string]string{}
		unique := map[string]bool{}
		requested := []string{}
		for _, text := range texts {
			key := reference.Key(text)
			if key == "" || reference.Length(text) > MaxResolveText {
				continue
			}
			keys[text] = key
			if !unique[key] {
				requested = append(requested, key)
				unique[key] = true
			}
		}
		owners, err := repo.KeyOwners(ctx, code, requested)
		if err != nil {
			return err
		}
		return resolveBatchOwners(ctx, repo, code, keys, owners, result)
	})
	return result, err
}

func resolveBatchOwners(
	ctx context.Context,
	repo reference.Repository,
	code string,
	keys map[string]string,
	owners map[string]ids.UUID,
	result map[string]ids.UUID,
) error {
	unique := map[ids.UUID]bool{}
	requested := []ids.UUID{}
	for _, owner := range owners {
		if !unique[owner] {
			requested = append(requested, owner)
			unique[owner] = true
		}
	}
	entries, err := repo.Entries(ctx, code, requested)
	if err != nil {
		return err
	}
	resolvable := map[ids.UUID]bool{}
	for _, entry := range entries {
		resolvable[entry.ID()] = entry.Resolvable()
	}
	for text, key := range keys {
		if owner := owners[key]; resolvable[owner] {
			result[text] = owner
		}
	}
	return nil
}
