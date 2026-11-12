package reference

import (
	"context"
	"errors"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// DictionaryView — описание доступного потребителю словаря.
type DictionaryView struct {
	Code     string         `json:"code"`
	Kind     reference.Kind `json:"kind"`
	Revision int64          `json:"revision"`
}

// CandidateCollection — итог обхода с областями, не уложившимися в контракт поиска.
type CandidateCollection struct {
	Created int
	Skipped []taxonomy.CandidateScope
}

type catalogObservation struct {
	Attribute  string   `json:"attribute"`
	Categories []string `json:"categories"`
	Documents  int64    `json:"documents"`
	Sellers    int64    `json:"sellers"`
	ObservedAt string   `json:"observedAt"`
}

type candidateMetadata struct {
	Catalog catalogObservation `json:"catalog"`
}

// Dictionaries читает только заголовки словарей, без подсчёта записей.
func (s *Service) Dictionaries(ctx context.Context) ([]DictionaryView, error) {
	out := []DictionaryView{}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		dictionaries, err := tx.Reference().Dictionaries(ctx)
		if err != nil {
			return err
		}
		for _, dict := range dictionaries {
			out = append(out, DictionaryView{Code: dict.Code(), Kind: dict.State().Kind, Revision: dict.Revision()})
		}
		return nil
	})
	return out, err
}

// CollectCandidates продолжает остальные области после сбоя и возвращает общий итог.
func (s *Service) CollectCandidates(
	ctx context.Context,
	code string,
	search ports.ReferenceCandidates,
) (CandidateCollection, error) {
	var result CandidateCollection
	var definition taxonomy.Definition
	var kind reference.Kind
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		dict, err := tx.Reference().Dictionary(ctx, code)
		if err != nil {
			return err
		}
		kind = dict.State().Kind
		definition, err = tx.Taxonomy().Current(ctx)
		return err
	})
	if err != nil {
		return result, err
	}
	failures := []error{}
	for _, scope := range definition.CandidateScopes(code, kind) {
		if scope.Dictionaries[len(scope.Dictionaries)-1] != code {
			continue
		}
		queries, skipped := candidateQueries(definition, scope)
		result.Skipped = append(result.Skipped, skipped...)
		for _, query := range queries {
			count, err := s.collectCandidateScope(ctx, code, query, search)
			result.Created += count
			if err != nil {
				failures = append(failures, fmt.Errorf(
					"атрибут %s, разделы %v, исключения %v: %w",
					query.Attribute,
					query.Categories,
					query.ExcludeCategories,
					err,
				))
			}
		}
	}
	return result, errors.Join(failures...)
}

func (s *Service) collectCandidateScope(
	ctx context.Context,
	code string,
	scope taxonomy.CandidateScope,
	search ports.ReferenceCandidates,
) (int, error) {
	created := 0
	seen := map[string]bool{}
	query := ports.AttributeValuesQuery{
		Attribute:         scope.Attribute,
		Categories:        scope.Categories,
		ExcludeCategories: scope.ExcludeCategories,
		Size:              500,
	}
	for {
		page, err := search.AttributeValues(ctx, query)
		if err != nil {
			return created, err
		}
		count, err := s.createCandidates(ctx, code, scope, page.Items)
		created += count
		if err != nil {
			return created, err
		}
		if page.After == "" {
			return created, nil
		}
		if seen[page.After] {
			return created, reference.ErrInvalid.WithMessage("повтор курсора поиска")
		}
		seen[page.After] = true
		query.After = page.After
	}
}
