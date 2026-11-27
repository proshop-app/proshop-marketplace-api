package reference

import (
	"context"
	"math"
	"strconv"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// confidenceExact — точное совпадение ключа; нечёткое совпадение match не даёт.
const confidenceExact = "exact"

// Get — запись по id; объединённая отдаётся вместе с выжившей, ссылка не ломается.
func (s *Service) Get(ctx context.Context, code string, id ids.UUID) (EntryView, error) {
	var view EntryView
	err := s.read(ctx, code, func(ctx context.Context, repo reference.Repository, dict *reference.Dictionary) error {
		entry, err := repo.Entry(ctx, code, id)
		if err != nil {
			return err
		}
		view = EntryView{Entry: entry.State(), Revision: dict.Revision()}
		if entry.Status() == reference.StatusMerged {
			survivor, err := repo.Entry(ctx, code, entry.MergedInto())
			if err != nil {
				return err
			}
			state := survivor.State()
			view.Resolved = &state
		}
		return nil
	})
	return view, err
}

// List — страница записей: очередь кандидатов идёт по частоте встреч.
func (s *Service) List(ctx context.Context, code string, filter reference.ListFilter) (EntryPage, error) {
	if filter.Limit <= 0 {
		filter.Limit = defaultPage
	}
	if filter.Limit > maxPage || filter.Offset < 0 {
		message := "limit — до " + strconv.Itoa(maxPage) + ", offset — не меньше 0"
		return EntryPage{}, reference.ErrInvalid.WithMessage(message)
	}
	var page EntryPage
	err := s.read(ctx, code, func(ctx context.Context, repo reference.Repository, dict *reference.Dictionary) error {
		entries, total, err := repo.List(ctx, code, filter)
		if err != nil {
			return err
		}
		page = EntryPage{Entries: states(entries), Total: total, Revision: dict.Revision()}
		return nil
	})
	return page, err
}

// Resolve — точное совпадение ключа варианта даёт match; иначе до десяти
// кандидатов по префиксу и триграммам, которые никогда не привязываются сами.
// Отклонённая и выведенная запись совпадением не считается. Словарь
// проверяется до текста: отказ invalid всегда относится к существующему словарю.
func (s *Service) Resolve(ctx context.Context, code, text string) (Resolution, error) {
	result := Resolution{Dictionary: code, Candidates: []Match{}}
	err := s.read(ctx, code, func(ctx context.Context, repo reference.Repository, dict *reference.Dictionary) error {
		if err := dict.CheckNormalizer(); err != nil {
			return err
		}
		if reference.Length(text) > MaxResolveText {
			message := "text — не длиннее " + strconv.Itoa(MaxResolveText) + " символов"
			return reference.ErrInvalid.WithMessage(message)
		}
		key := reference.Key(text)
		if key == "" {
			return reference.ErrInvalid.WithMessage("text пустой")
		}
		result.Key = key
		result.Revision, result.Normalizer = dict.Revision(), dict.Normalizer()
		owners, err := repo.KeyOwners(ctx, code, []string{key})
		if err != nil {
			return err
		}
		if owner, ok := owners[key]; ok {
			entry, err := repo.Entry(ctx, code, owner)
			if err != nil {
				return err
			}
			if entry.Resolvable() {
				state := entry.State()
				result.Match = &Match{
					EntryID:    state.ID,
					Canonical:  state.CanonicalName,
					AliasKey:   key,
					Status:     state.Status,
					Confidence: confidenceExact,
					Weight:     1,
				}
				return nil
			}
		}
		candidates, err := repo.Candidates(ctx, code, key, maxCandidates)
		if err != nil {
			return err
		}
		for _, candidate := range candidates {
			result.Candidates = append(result.Candidates, Match{
				EntryID:   candidate.EntryID,
				Canonical: candidate.Canonical,
				AliasKey:  candidate.AliasKey,
				Status:    candidate.Status,
				Weight:    math.Round(candidate.Similarity*1000) / 1000,
			})
		}
		return nil
	})
	return result, err
}

// Stats — ревизия и длина очереди кандидатов каждого словаря.
func (s *Service) Stats(ctx context.Context) ([]Stats, error) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	var out []Stats
	updated := map[string]Stats{}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		repo := tx.Reference()
		dictionaries, err := repo.Dictionaries(ctx)
		if err != nil {
			return err
		}
		changed := []string{}
		for _, dict := range dictionaries {
			previous, found := s.stats[dict.Code()]
			if !found || previous.Revision != dict.Revision() || previous.Entries != dict.State().Entries {
				changed = append(changed, dict.Code())
			}
		}
		counts, err := repo.CandidateCounts(ctx, changed)
		if err != nil {
			return err
		}
		for _, dict := range dictionaries {
			row := Stats{
				Dictionary: dict.Code(),
				Revision:   dict.Revision(),
				Entries:    dict.State().Entries,
				Candidates: s.stats[dict.Code()].Candidates,
			}
			if count, changed := counts[dict.Code()]; changed {
				row.Candidates = count
			}
			out = append(out, row)
			updated[row.Dictionary] = row
		}
		return nil
	})
	if err == nil {
		s.stats = updated
	}
	return out, err
}

// read — чтение словаря без блокировки.
func (s *Service) read(
	ctx context.Context,
	code string,
	fn func(ctx context.Context, repo reference.Repository, dict *reference.Dictionary) error,
) error {
	if !reference.ValidCode(code) {
		return reference.ErrDictionaryNotFound
	}
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		repo := tx.Reference()
		dict, err := repo.Dictionary(ctx, code)
		if err != nil {
			return err
		}
		return fn(ctx, repo, dict)
	})
}

func states(entries []*reference.Entry) []reference.EntryState {
	out := make([]reference.EntryState, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.State())
	}
	return out
}
