package reference

import (
	"context"
	"strconv"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

// ImportResult — результат словаря; Created относится к заголовку, а не записям.
type ImportResult struct {
	Code           string `json:"code"`
	Created        bool   `json:"created"`
	Release        int64  `json:"release"`
	Revision       int64  `json:"revision"`
	Changed        bool   `json:"changed"`
	CreatedEntries int    `json:"createdEntries"`
	UpdatedEntries int    `json:"updatedEntries"`
	SkippedEntries int    `json:"skippedEntries"`
}

// DictionarySummary — операторский список с длиной очереди кандидатов.
type DictionarySummary struct {
	Code       string         `json:"code"`
	Kind       reference.Kind `json:"kind"`
	Revision   int64          `json:"revision"`
	Entries    int64          `json:"entries"`
	Candidates int64          `json:"candidates"`
}

// Import создаёт отсутствующие словари и применяет все поставки одной транзакцией.
// Сортировка кодов в PrepareImport задаёт единый порядок блокировок пакетов.
func (s *Service) Import(ctx context.Context, input reference.ImportPackage) ([]ImportResult, error) {
	prepared, err := reference.PrepareImport(input)
	if err != nil {
		return nil, err
	}
	results := make([]ImportResult, 0, len(prepared))
	err = s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for _, item := range prepared {
			result, err := s.importDictionary(ctx, tx, item)
			if err != nil {
				return err
			}
			results = append(results, result)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Service) importDictionary(
	ctx context.Context,
	tx ports.Tx,
	item reference.PreparedImport,
) (ImportResult, error) {
	d := item.Delivery
	created, err := tx.Reference().CreateDictionary(ctx, reference.DictionaryState{
		Code:       d.Dictionary,
		Kind:       item.Kind,
		AliasKinds: reference.AliasKinds,
		Limits:     reference.DefaultLimits(),
	})
	if err != nil {
		return ImportResult{}, err
	}
	result := reference.DeliveryResult{Dictionary: d.Dictionary, Release: d.Version}
	meta := reference.Meta{Actor: "delivery:" + strconv.FormatInt(d.Version, 10), Source: reference.SourceSeed}
	revision, err := s.mutateInTransaction(ctx, tx, d.Dictionary, func(ctx context.Context, m *session) error {
		if m.dict.State().Kind != item.Kind {
			return reference.ErrDeliveryConflict.WithMessage("нельзя изменить вид словаря: " + d.Dictionary)
		}
		return s.applyDelivery(ctx, m, d, meta, &result)
	})
	if err != nil {
		return ImportResult{}, err
	}
	return ImportResult{
		Code:           d.Dictionary,
		Created:        created,
		Release:        d.Version,
		Revision:       revision,
		Changed:        result.Changed,
		CreatedEntries: result.Created,
		UpdatedEntries: result.Updated,
		SkippedEntries: result.Skipped,
	}, nil
}

// DictionarySummaries читает счётчики и настройки одним согласованным сценарием.
func (s *Service) DictionarySummaries(ctx context.Context) ([]DictionarySummary, error) {
	out := []DictionarySummary{}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		dictionaries, err := tx.Reference().Dictionaries(ctx)
		if err != nil {
			return err
		}
		for _, dictionary := range dictionaries {
			candidates, err := tx.Reference().CountStatus(ctx, dictionary.Code(), reference.StatusCandidate)
			if err != nil {
				return err
			}
			state := dictionary.State()
			out = append(out, DictionarySummary{
				Code:       state.Code,
				Kind:       state.Kind,
				Revision:   state.Revision,
				Entries:    state.Entries,
				Candidates: candidates,
			})
		}
		return nil
	})
	return out, err
}
