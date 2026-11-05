package reference

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

// DictionaryView — описание доступного потребителю словаря.
type DictionaryView struct {
	Code     string         `json:"code"`
	Kind     reference.Kind `json:"kind"`
	Revision int64          `json:"revision"`
}

type catalogObservation struct {
	Documents  int64  `json:"documents"`
	Sellers    int64  `json:"sellers"`
	ObservedAt string `json:"observedAt"`
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

// CollectCandidates пропускает занятые ключи независимо от статуса владельца.
func (s *Service) CollectCandidates(ctx context.Context, code string, search ports.ReferenceCandidates) (int, error) {
	created := 0
	after := ""
	seen := map[string]bool{}
	for {
		page, err := search.AttributeValues(ctx, code, 500, after)
		if err != nil {
			return created, err
		}
		count, err := s.createCandidates(ctx, code, page.Items)
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
		after = page.After
	}
}
