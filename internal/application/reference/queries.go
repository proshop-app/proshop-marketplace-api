package reference

import (
	"context"
	"errors"
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
		return EntryPage{}, reference.ErrInvalid.WithMessage("limit — до " + strconv.Itoa(maxPage) + ", offset — не меньше 0")
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
// Отклонённая и выведенная запись совпадением не считается.
func (s *Service) Resolve(ctx context.Context, code, text string) (Resolution, error) {
	if reference.Length(text) > MaxResolveText {
		return Resolution{}, reference.ErrInvalid.WithMessage("text — не длиннее " + strconv.Itoa(MaxResolveText) + " символов")
	}
	key := reference.Key(text)
	if key == "" {
		return Resolution{}, reference.ErrInvalid.WithMessage("text пустой")
	}
	result := Resolution{Dictionary: code, Key: key, Candidates: []Match{}}
	err := s.read(ctx, code, func(ctx context.Context, repo reference.Repository, dict *reference.Dictionary) error {
		if err := dict.CheckNormalizer(); err != nil {
			return err
		}
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

// Snapshot — полный снимок без since, со since за горизонтом журнала или
// впереди головы; иначе дельта: текущие записи, затронутые после since, и
// удалённые id. Голова читается первой: запись новее головы безопасна, потому
// что следующая дельта отдаст её ещё раз.
func (s *Service) Snapshot(ctx context.Context, code string, since *int64) (Snapshot, error) {
	var snapshot Snapshot
	err := s.read(ctx, code, func(ctx context.Context, repo reference.Repository, dict *reference.Dictionary) error {
		snapshot = Snapshot{
			Dictionary: code,
			Revision:   dict.Revision(),
			Normalizer: dict.Normalizer(),
			Entries:    []reference.EntryState{},
			Removed:    []ids.UUID{},
		}
		horizon, err := repo.Horizon(ctx, code, s.clock.Now().Add(-s.config.Horizon))
		if err != nil {
			return err
		}
		if since == nil || *since < horizon || *since > dict.Revision() {
			snapshot.Full = true
			entries, err := repo.AllEntries(ctx, code)
			snapshot.Entries = states(entries)
			return err
		}
		changed, err := repo.ChangedEntries(ctx, code, *since, dict.Revision())
		if err != nil {
			return err
		}
		entries, err := repo.Entries(ctx, code, changed)
		if err != nil {
			return err
		}
		present := map[ids.UUID]bool{}
		for _, entry := range entries {
			present[entry.ID()] = true
		}
		for _, id := range changed {
			if !present[id] {
				snapshot.Removed = append(snapshot.Removed, id)
			}
		}
		snapshot.Entries = states(entries)
		return nil
	})
	return snapshot, err
}

// Stats — ревизия и длина очереди кандидатов каждого словаря.
func (s *Service) Stats(ctx context.Context) ([]Stats, error) {
	var out []Stats
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		repo := tx.Reference()
		dictionaries, err := repo.Dictionaries(ctx)
		if err != nil {
			return err
		}
		for _, dict := range dictionaries {
			candidates, err := repo.CountStatus(ctx, dict.Code(), reference.StatusCandidate)
			if err != nil {
				return err
			}
			state := dict.State()
			out = append(out, Stats{
				Dictionary: dict.Code(),
				Revision:   state.Revision,
				Entries:    state.Entries,
				Candidates: candidates,
			})
		}
		return nil
	})
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

// isNotFound — записи нет; откат по журналу считает это отсутствием образа.
func isNotFound(err error) bool {
	return errors.Is(err, reference.ErrEntryNotFound)
}
