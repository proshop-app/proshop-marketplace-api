package reference

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// reviewBatch ограничивает размер одного прямого/обратного образа журнала.
const (
	reviewBatch = 500
	// Помимо числа записей ограничены JSON-образы: 50 вариантов и metadata могут быть большими.
	maxReviewImages = 1 << 20
)

// ReviewCommand — одно решение оператора над выбором при известной голове словаря.
type ReviewCommand struct {
	Dictionary string
	IfMatch    int64
	Status     reference.Status
	Selection  reference.ReviewSelection
	Meta       reference.Meta
}

// ReviewResult сообщает только фактически зафиксированные изменения одной транзакции.
type ReviewResult struct {
	Dictionary string
	Revision   int64
	Changed    int
}

// Review подтверждает или отклоняет кандидатов атомарно: любой конфликт отменяет весь выбор.
func (s *Service) Review(ctx context.Context, command ReviewCommand) (ReviewResult, error) {
	if command.IfMatch < 0 {
		return ReviewResult{}, reference.ErrInvalid.WithMessage("ревизия словаря неотрицательна")
	}
	if err := reference.ValidateReview(command.Status, command.Selection); err != nil {
		return ReviewResult{}, err
	}
	result := ReviewResult{Dictionary: command.Dictionary}
	revision, err := s.mutate(ctx, command.Dictionary, func(ctx context.Context, m *session) error {
		if m.dict.Revision() != command.IfMatch {
			return reference.ErrDictionaryChanged
		}
		selected, err := reviewSelection(ctx, m, command.Selection)
		if err != nil {
			return err
		}
		for start := 0; start < len(selected); start += reviewBatch {
			batch := selected[start:min(start+reviewBatch, len(selected))]
			entries, err := candidateEntries(ctx, m, batch)
			if err != nil {
				return err
			}
			if err := recordReviewEntries(ctx, m, entries, command.Status, command.Meta); err != nil {
				return err
			}
			result.Changed += len(entries)
		}
		return nil
	})
	if err != nil {
		// Предварительно обработанные пачки могли откатиться: наружу частичный счётчик не выходит.
		return ReviewResult{}, err
	}
	result.Revision = revision
	return result, nil
}

func reviewSelection(ctx context.Context, m *session, selection reference.ReviewSelection) ([]ids.UUID, error) {
	if selection.Mode == reference.ReviewIDs {
		selected := slices.Clone(selection.IDs)
		slices.Sort(selected)
		return selected, nil
	}
	// Исключение тоже принадлежит тому же снимку: случайный чужой id не игнорируется.
	for start := 0; start < len(selection.ExcludedIDs); start += reviewBatch {
		batch := selection.ExcludedIDs[start:min(start+reviewBatch, len(selection.ExcludedIDs))]
		if _, err := candidateEntries(ctx, m, batch); err != nil {
			return nil, err
		}
	}
	selected, err := m.repo.CandidateEntryIDs(
		ctx,
		m.dict.Code(),
		selection.ExcludedIDs,
		reference.MaxReviewEntries+1,
	)
	if err != nil {
		return nil, err
	}
	if len(selected) > reference.MaxReviewEntries {
		return nil, reference.ErrLimit.WithMessage("массовое решение ограничено 10000 кандидатами")
	}
	return selected, nil
}

func candidateEntries(ctx context.Context, m *session, selected []ids.UUID) ([]*reference.Entry, error) {
	entries, err := m.repo.Entries(ctx, m.dict.Code(), selected)
	if err != nil {
		return nil, err
	}
	if len(entries) != len(selected) {
		return nil, reference.ErrSelectionChanged
	}
	for _, entry := range entries {
		if entry.Status() != reference.StatusCandidate {
			return nil, reference.ErrSelectionChanged
		}
	}
	return entries, nil
}

// recordReviewEntries удерживает небольшой прямой/обратный образ даже у богатых записей.
func recordReviewEntries(
	ctx context.Context,
	m *session,
	entries []*reference.Entry,
	status reference.Status,
	meta reference.Meta,
) error {
	draft := reference.NewDraft(m.dict.Code())
	var primary ids.UUID
	size := 0
	for _, entry := range entries {
		raw, err := json.Marshal(entry.State())
		if err != nil {
			return err
		}
		// Два образа со статусом той же или меньшей длины и обёртки Image с id.
		weight := 2*len(raw) + 128
		if size > 0 && size+weight > maxReviewImages {
			if _, err := m.record(ctx, draft, reference.ChangeUpdated, primary, meta, 0); err != nil {
				return err
			}
			draft = reference.NewDraft(m.dict.Code())
			size = 0
		}
		if size == 0 {
			primary = entry.ID()
		}
		draft.Track(entry)
		if _, err := entry.ChangeStatus(status, m.now); err != nil {
			return err
		}
		size += weight
	}
	if size == 0 {
		return nil
	}
	_, err := m.record(ctx, draft, reference.ChangeUpdated, primary, meta, 0)
	return err
}
