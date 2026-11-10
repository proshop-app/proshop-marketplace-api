package fake

import (
	"context"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// trigramThreshold — порог похожести, как pg_trgm.similarity_threshold по умолчанию.
const trigramThreshold = 0.3

// referenceData — реестр в памяти; копируется на каждую транзакцию вместе с остальным.
type referenceData struct {
	dictionaries map[string]reference.DictionaryState
	entries      map[ids.UUID]reference.EntryState
	changes      []reference.Change
	nextRevision int64
	deliveries   []reference.DeliveryRecord
	presence     map[ids.UUID][]reference.Presence
}

type references struct{ d *data }

// Publisher — брокер в памяти: запоминает опубликованные сообщения.
type Publisher struct {
	mu       sync.Mutex
	messages []ports.Message
	// Err, если задан, возвращается вместо публикации.
	Err error
}

func newReferenceData() *referenceData {
	return &referenceData{
		dictionaries: map[string]reference.DictionaryState{},
		entries:      map[ids.UUID]reference.EntryState{},
		presence:     map[ids.UUID][]reference.Presence{},
	}
}

func (r *referenceData) clone() *referenceData {
	return &referenceData{
		dictionaries: maps.Clone(r.dictionaries),
		entries:      maps.Clone(r.entries),
		changes:      slices.Clone(r.changes),
		nextRevision: r.nextRevision,
		deliveries:   slices.Clone(r.deliveries),
		presence:     maps.Clone(r.presence),
	}
}

func (t *tx) Reference() reference.Repository { return references{t.data} }

// SeedReferenceDictionary заводит словарь для тестов сценариев.
func (s *Store) SeedReferenceDictionary(state reference.DictionaryState) {
	s.read(func(d *data) { d.reference.dictionaries[state.Code] = state })
}

// ReferenceEntry — запись реестра, если есть.
func (s *Store) ReferenceEntry(id ids.UUID) (reference.EntryState, bool) {
	var (
		state reference.EntryState
		ok    bool
	)
	s.read(func(d *data) { state, ok = d.reference.entries[id] })
	return state, ok
}

// ReferenceChanges — журнал реестра по возрастанию ревизии.
func (s *Store) ReferenceChanges() []reference.Change {
	var out []reference.Change
	s.read(func(d *data) { out = slices.Clone(d.reference.changes) })
	return out
}

// AgeReferenceChanges сдвигает время записей журнала назад: так тест видит горизонт дельты.
func (s *Store) AgeReferenceChanges(upTo int64, by time.Duration) {
	s.read(func(d *data) {
		for i := range d.reference.changes {
			if d.reference.changes[i].Revision <= upTo {
				d.reference.changes[i].At = d.reference.changes[i].At.Add(-by)
			}
		}
	})
}

// ReferencePresence — присутствие записи в узлах дерева.
func (s *Store) ReferencePresence(id ids.UUID) []reference.Presence {
	var out []reference.Presence
	s.read(func(d *data) { out = slices.Clone(d.reference.presence[id]) })
	return out
}

func (r references) CreateDictionary(_ context.Context, state reference.DictionaryState) (bool, error) {
	if _, exists := r.d.reference.dictionaries[state.Code]; exists {
		return false, nil
	}
	r.d.reference.dictionaries[state.Code] = reference.RestoreDictionary(state).State()
	return true, nil
}

func (r references) LockDictionary(ctx context.Context, code string) (*reference.Dictionary, error) {
	return r.Dictionary(ctx, code)
}

func (r references) Dictionary(_ context.Context, code string) (*reference.Dictionary, error) {
	state, ok := r.d.reference.dictionaries[code]
	if !ok {
		return nil, reference.ErrDictionaryNotFound
	}
	return reference.RestoreDictionary(state), nil
}

func (r references) Dictionaries(context.Context) ([]*reference.Dictionary, error) {
	codes := slices.Sorted(maps.Keys(r.d.reference.dictionaries))
	out := make([]*reference.Dictionary, 0, len(codes))
	for _, code := range codes {
		out = append(out, reference.RestoreDictionary(r.d.reference.dictionaries[code]))
	}
	return out, nil
}

func (r references) SaveDictionary(_ context.Context, dict *reference.Dictionary) error {
	r.d.reference.dictionaries[dict.Code()] = dict.State()
	return nil
}

func (r references) Append(_ context.Context, change reference.Change) (int64, error) {
	r.d.reference.nextRevision++
	change.Revision = r.d.reference.nextRevision
	r.d.reference.changes = append(r.d.reference.changes, change)
	return change.Revision, nil
}

func (r references) Changes(_ context.Context, dictionary string, from, to int64) ([]reference.Change, error) {
	var out []reference.Change
	for _, change := range r.d.reference.changes {
		if change.Dictionary == dictionary && change.Revision > from && change.Revision <= to {
			out = append(out, change)
		}
	}
	return out, nil
}

func (r references) ChangedEntries(
	ctx context.Context,
	dictionary string,
	since, until int64,
) ([]ids.UUID, error) {
	changes, _ := r.Changes(ctx, dictionary, since, until)
	seen := map[ids.UUID]bool{}
	var out []ids.UUID
	for _, change := range changes {
		for _, id := range change.EntryIDs {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out, nil
}

func (r references) Horizon(_ context.Context, dictionary string, before time.Time) (int64, error) {
	var horizon int64
	for _, change := range r.d.reference.changes {
		if change.Dictionary == dictionary && change.At.Before(before) {
			horizon = max(horizon, change.Revision)
		}
	}
	return horizon, nil
}

func (r references) Entry(_ context.Context, dictionary string, id ids.UUID) (*reference.Entry, error) {
	state, ok := r.d.reference.entries[id]
	if !ok || state.Dictionary != dictionary {
		return nil, reference.ErrEntryNotFound
	}
	return reference.RestoreEntry(state), nil
}

func (r references) Entries(ctx context.Context, dictionary string, list []ids.UUID) ([]*reference.Entry, error) {
	var out []*reference.Entry
	for _, id := range list {
		if e, err := r.Entry(ctx, dictionary, id); err == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

func (r references) AllEntries(_ context.Context, dictionary string) ([]*reference.Entry, error) {
	return r.sorted(dictionary, ""), nil
}

func (r references) List(
	_ context.Context,
	dictionary string,
	filter reference.ListFilter,
) ([]*reference.Entry, int, error) {
	all := r.sorted(dictionary, filter.Status)
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i].State(), all[j].State()
		if a.Occurrences != b.Occurrences {
			return a.Occurrences > b.Occurrences
		}
		return a.CanonicalKey < b.CanonicalKey
	})
	total := len(all)
	start := min(filter.Offset, total)
	end := min(start+filter.Limit, total)
	return all[start:end], total, nil
}

func (r references) MergedInto(_ context.Context, dictionary string, id ids.UUID) ([]*reference.Entry, error) {
	var out []*reference.Entry
	for _, e := range r.sorted(dictionary, reference.StatusMerged) {
		if e.MergedInto() == id {
			out = append(out, e)
		}
	}
	return out, nil
}

func (r references) EntryByAlias(_ context.Context, dictionary string, alias ids.UUID) (*reference.Entry, error) {
	for _, e := range r.sorted(dictionary, "") {
		for _, a := range e.State().Aliases {
			if a.ID == alias {
				return e, nil
			}
		}
	}
	return nil, reference.ErrAliasNotFound
}

func (r references) KeyOwners(_ context.Context, dictionary string, keys []string) (map[string]ids.UUID, error) {
	wanted := map[string]bool{}
	for _, key := range keys {
		wanted[key] = true
	}
	owners := map[string]ids.UUID{}
	for _, state := range r.d.reference.entries {
		if state.Dictionary != dictionary {
			continue
		}
		for _, alias := range state.Aliases {
			if wanted[alias.Key] {
				owners[alias.Key] = state.ID
			}
		}
	}
	return owners, nil
}

func (r references) Candidates(
	_ context.Context,
	dictionary, key string,
	limit int,
) ([]reference.Candidate, error) {
	best := map[ids.UUID]reference.Candidate{}
	for _, e := range r.sorted(dictionary, "") {
		state := e.State()
		if state.Status != reference.StatusCandidate && state.Status != reference.StatusConfirmed {
			continue
		}
		for _, alias := range state.Aliases {
			candidate := reference.Candidate{
				EntryID:    state.ID,
				Canonical:  state.CanonicalName,
				AliasKey:   alias.Key,
				Status:     state.Status,
				Prefix:     strings.HasPrefix(alias.Key, key),
				Similarity: similarity(alias.Key, key),
			}
			if !candidate.Prefix && candidate.Similarity < trigramThreshold {
				continue
			}
			if current, ok := best[state.ID]; !ok || better(candidate, current) {
				best[state.ID] = candidate
			}
		}
	}
	out := slices.Collect(maps.Values(best))
	sort.Slice(out, func(i, j int) bool { return better(out[i], out[j]) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r references) CountStatus(_ context.Context, dictionary string, status reference.Status) (int64, error) {
	return int64(len(r.sorted(dictionary, status))), nil
}

func (r references) Save(_ context.Context, saved, deleted []*reference.Entry) error {
	for _, e := range deleted {
		stored, ok := r.d.reference.entries[e.ID()]
		if !ok || stored.Version != e.Origin().Version {
			return reference.ErrEntryChanged
		}
		delete(r.d.reference.entries, e.ID())
		delete(r.d.reference.presence, e.ID())
	}
	for _, e := range saved {
		stored, ok := r.d.reference.entries[e.ID()]
		origin := e.Origin()
		if e.IsNew() == ok || (ok && (stored.Version != origin.Version || stored.Status != origin.Status)) {
			return reference.ErrEntryChanged
		}
	}
	// ключи проверяются после всех записей изменения: объединение переносит вариант между записями
	next := maps.Clone(r.d.reference.entries)
	for _, e := range saved {
		next[e.ID()] = e.State()
	}
	if err := uniqueKeys(next); err != nil {
		return err
	}
	r.d.reference.entries = next
	for _, e := range saved {
		e.MarkSaved()
	}
	return nil
}

func (r references) LatestDelivery(_ context.Context, dictionary string) (*reference.DeliveryRecord, error) {
	var latest *reference.DeliveryRecord
	for i, record := range r.d.reference.deliveries {
		if record.Dictionary == dictionary && (latest == nil || record.Version > latest.Version) {
			latest = &r.d.reference.deliveries[i]
		}
	}
	if latest == nil {
		return nil, nil
	}
	copied := *latest
	return &copied, nil
}

func (r references) RecordDelivery(_ context.Context, record reference.DeliveryRecord) error {
	r.d.reference.deliveries = append(r.d.reference.deliveries, record)
	return nil
}

func (r references) MarkDeliveryRollback(_ context.Context, dictionary string, version, revision int64) error {
	for i, record := range r.d.reference.deliveries {
		if record.Dictionary == dictionary && record.Version == version {
			r.d.reference.deliveries[i].RolledBack = true
			r.d.reference.deliveries[i].RollbackRevision = revision
		}
	}
	return nil
}

func (r references) ReplacePresence(_ context.Context, entry ids.UUID, rows []reference.Presence, _ time.Time) error {
	if _, ok := r.d.reference.entries[entry]; !ok {
		return reference.ErrEntryNotFound
	}
	r.d.reference.presence[entry] = slices.Clone(rows)
	return nil
}

func (r references) sorted(dictionary string, status reference.Status) []*reference.Entry {
	var out []*reference.Entry
	for _, state := range r.d.reference.entries {
		if state.Dictionary == dictionary && (status == "" || state.Status == status) {
			out = append(out, reference.RestoreEntry(state))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// Publish запоминает сообщение или возвращает Err.
func (p *Publisher) Publish(_ context.Context, message ports.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Err != nil {
		return p.Err
	}
	p.messages = append(p.messages, message)
	return nil
}

// Messages — опубликованные сообщения.
func (p *Publisher) Messages() []ports.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.messages)
}

func uniqueKeys(entries map[ids.UUID]reference.EntryState) error {
	owners := map[string]ids.UUID{}
	for _, state := range entries {
		for _, alias := range state.Aliases {
			key := state.Dictionary + "\x00" + alias.Key
			if owner, taken := owners[key]; taken && owner != state.ID {
				return reference.ErrKeyTaken
			}
			owners[key] = state.ID
		}
	}
	return nil
}

func better(a, b reference.Candidate) bool {
	if a.Prefix != b.Prefix {
		return a.Prefix
	}
	if a.Similarity != b.Similarity {
		return a.Similarity > b.Similarity
	}
	return a.AliasKey < b.AliasKey
}

// similarity — похожесть по триграммам, как similarity() в pg_trgm: каждое
// слово дополняется двумя пробелами слева и одним справа.
func similarity(a, b string) float64 {
	left, right := trigrams(a), trigrams(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	common := 0
	for gram := range left {
		if right[gram] {
			common++
		}
	}
	return float64(common) / float64(len(left)+len(right)-common)
}

func trigrams(text string) map[string]bool {
	set := map[string]bool{}
	for _, word := range strings.Fields(text) {
		runes := []rune("  " + word + " ")
		for i := 0; i+3 <= len(runes); i++ {
			set[string(runes[i:i+3])] = true
		}
	}
	return set
}
