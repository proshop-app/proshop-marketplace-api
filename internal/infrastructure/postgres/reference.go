package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	dictionaryColumns = `code, kind, identity, alias_kinds, limits, revision, entries`

	// lockDictionary сериализует мутации словаря: ревизии одного словаря
	// фиксируются в порядке возрастания, дельта since ничего не пропускает.
	lockDictionary = `
		SELECT ` + dictionaryColumns + `
		FROM reference.dictionaries
		WHERE code = $1
		FOR UPDATE`

	selectDictionary = `
		SELECT ` + dictionaryColumns + `
		FROM reference.dictionaries
		WHERE code = $1`

	selectDictionaries = `
		SELECT ` + dictionaryColumns + `
		FROM reference.dictionaries
		ORDER BY code`

	updateDictionary = `
		UPDATE reference.dictionaries
		SET revision = $2,
			entries = $3,
			updated_at = now()
		WHERE code = $1`

	entryColumns = `
		e.id, e.dictionary_code, e.canonical_name, e.canonical_key, e.status, e.merged_into, e.source,
		e.metadata, e.occurrences, e.revision, e.version, e.created_at, e.updated_at`

	selectReferenceEntry = `
		SELECT ` + entryColumns + `
		FROM reference.entries e
		WHERE e.dictionary_code = $1 AND e.id = $2`

	selectReferenceEntries = `
		SELECT ` + entryColumns + `
		FROM reference.entries e
		WHERE e.dictionary_code = $1 AND e.id = ANY($2::uuid[])
		ORDER BY e.id`

	selectAllReferenceEntries = `
		SELECT ` + entryColumns + `
		FROM reference.entries e
		WHERE e.dictionary_code = $1
		ORDER BY e.id`

	listReferenceEntries = `
		SELECT ` + entryColumns + `
		FROM reference.entries e
		WHERE e.dictionary_code = $1 AND ($2 = '' OR e.status = $2)
		ORDER BY e.occurrences DESC, e.canonical_key, e.id
		LIMIT $3 OFFSET $4`

	countReferenceEntries = `
		SELECT count(*)
		FROM reference.entries
		WHERE dictionary_code = $1 AND ($2 = '' OR status = $2)`

	selectMergedInto = `
		SELECT ` + entryColumns + `
		FROM reference.entries e
		WHERE e.dictionary_code = $1 AND e.merged_into = $2
		ORDER BY e.id`

	selectEntryByAlias = `
		SELECT ` + entryColumns + `
		FROM reference.entries e
		JOIN reference.aliases a ON a.entry_id = e.id
		WHERE a.dictionary_code = $1 AND a.id = $2`

	selectKeyOwners = `
		SELECT key, entry_id
		FROM reference.aliases
		WHERE dictionary_code = $1 AND key = ANY($2::text[])`

	selectEntryAliases = `
		SELECT entry_id, id, text, key, kind, COALESCE(locale, ''), source, weight
		FROM reference.aliases
		WHERE entry_id = ANY($1::uuid[])
		ORDER BY entry_id, key`

	// selectCandidates — лучший вариант каждой записи: сначала совпадение по
	// префиксу, затем похожесть по триграммам (оператор % с порогом pg_trgm).
	selectCandidates = `
		SELECT id, canonical_name, key, status, prefix, score
		FROM (
			SELECT DISTINCT ON (e.id)
				e.id, e.canonical_name, a.key, e.status,
				a.key LIKE $3 ESCAPE '\' AS prefix,
				similarity(a.key, $2) AS score
			FROM reference.aliases a
			JOIN reference.entries e ON e.id = a.entry_id
			WHERE a.dictionary_code = $1
				AND e.status IN ('candidate', 'confirmed')
				AND (a.key LIKE $3 ESCAPE '\' OR a.key % $2)
			ORDER BY e.id, prefix DESC, score DESC, a.key
		) best
		ORDER BY prefix DESC, score DESC, key
		LIMIT $4`

	countReferenceStatus = `
		SELECT count(*)
		FROM reference.entries
		WHERE dictionary_code = $1 AND status = $2`

	insertReferenceEntry = `
		INSERT INTO reference.entries (
			id, dictionary_code, canonical_name, canonical_key, status, merged_into, source,
			metadata, occurrences, revision, version, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`

	updateReferenceEntry = `
		UPDATE reference.entries
		SET canonical_name = $4,
			canonical_key = $5,
			status = $6,
			merged_into = $7,
			source = $8,
			metadata = $9,
			occurrences = $10,
			revision = $11,
			version = $12,
			updated_at = $13
		WHERE id = $1 AND status = $2 AND version = $3`

	deleteReferenceEntry = `
		DELETE FROM reference.entries
		WHERE id = $1 AND status = $2 AND version = $3`

	insertReferenceAlias = `
		INSERT INTO reference.aliases (id, entry_id, dictionary_code, text, key, kind, locale, source, weight)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9)`

	updateReferenceAlias = `
		UPDATE reference.aliases
		SET text = $3,
			key = $4,
			kind = $5,
			locale = NULLIF($6, ''),
			source = $7,
			weight = $8
		WHERE id = $1 AND entry_id = $2`

	deleteReferenceAlias = `
		DELETE FROM reference.aliases
		WHERE id = $1 AND entry_id = $2`

	constraintAliasKey = "aliases_dictionary_key_key"
)

// References — реестр справочных сущностей в схеме reference.
type References struct {
	q querier
}

// saveCheck — чего ждать от оператора пачки сохранения.
type saveCheck struct {
	subject string
	// conditional — ноль строк значит, что запись изменили параллельно.
	conditional bool
	// insert — нарушение уникальности строки записи значит то же.
	insert bool
	// key — ключ варианта: нарушение уникальности значит занятое написание.
	key string
}

// LockDictionary — словарь с блокировкой строки до конца транзакции.
func (r *References) LockDictionary(ctx context.Context, code string) (*reference.Dictionary, error) {
	return r.dictionary(ctx, lockDictionary, code)
}

// Dictionary — словарь без блокировки.
func (r *References) Dictionary(ctx context.Context, code string) (*reference.Dictionary, error) {
	return r.dictionary(ctx, selectDictionary, code)
}

// Dictionaries — все словари по коду.
func (r *References) Dictionaries(ctx context.Context) ([]*reference.Dictionary, error) {
	rows, err := r.q.Query(ctx, selectDictionaries)
	if err != nil {
		return nil, fmt.Errorf("словари реестра: %w", err)
	}
	defer rows.Close()
	var out []*reference.Dictionary
	for rows.Next() {
		dict, err := scanDictionary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dict)
	}
	return out, rows.Err()
}

// SaveDictionary переносит голову журнала и счётчик записей; строка уже заблокирована.
func (r *References) SaveDictionary(ctx context.Context, dict *reference.Dictionary) error {
	state := dict.State()
	if _, err := r.q.Exec(ctx, updateDictionary, state.Code, state.Revision, state.Entries); err != nil {
		return fmt.Errorf("словарь %s: %w", state.Code, err)
	}
	return nil
}

// Entry — запись словаря с вариантами.
func (r *References) Entry(ctx context.Context, dictionary string, id ids.UUID) (*reference.Entry, error) {
	if _, err := ids.ParseUUID(string(id)); err != nil {
		return nil, reference.ErrEntryNotFound
	}
	entries, err := r.entries(ctx, selectReferenceEntry, dictionary, string(id))
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, reference.ErrEntryNotFound
	}
	return entries[0], nil
}

// Entries — записи по id; отсутствующих в ответе нет.
func (r *References) Entries(ctx context.Context, dictionary string, list []ids.UUID) ([]*reference.Entry, error) {
	if len(list) == 0 {
		return nil, nil
	}
	return r.entries(ctx, selectReferenceEntries, dictionary, uuidStrings(list))
}

// AllEntries — все записи словаря для полного снимка.
func (r *References) AllEntries(ctx context.Context, dictionary string) ([]*reference.Entry, error) {
	return r.entries(ctx, selectAllReferenceEntries, dictionary)
}

// List — страница записей по частоте встреч.
func (r *References) List(
	ctx context.Context,
	dictionary string,
	filter reference.ListFilter,
) ([]*reference.Entry, int, error) {
	var total int
	status := string(filter.Status)
	if err := r.q.QueryRow(ctx, countReferenceEntries, dictionary, status).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("записи %s: %w", dictionary, err)
	}
	entries, err := r.entries(ctx, listReferenceEntries, dictionary, status, filter.Limit, filter.Offset)
	return entries, total, err
}

// MergedInto — записи, объединённые в id.
func (r *References) MergedInto(ctx context.Context, dictionary string, id ids.UUID) ([]*reference.Entry, error) {
	return r.entries(ctx, selectMergedInto, dictionary, string(id))
}

// EntryByAlias — запись, которой принадлежит вариант.
func (r *References) EntryByAlias(ctx context.Context, dictionary string, alias ids.UUID) (*reference.Entry, error) {
	if _, err := ids.ParseUUID(string(alias)); err != nil {
		return nil, reference.ErrAliasNotFound
	}
	entries, err := r.entries(ctx, selectEntryByAlias, dictionary, string(alias))
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, reference.ErrAliasNotFound
	}
	return entries[0], nil
}

// KeyOwners — владельцы занятых ключей.
func (r *References) KeyOwners(ctx context.Context, dictionary string, keys []string) (map[string]ids.UUID, error) {
	rows, err := r.q.Query(ctx, selectKeyOwners, dictionary, keys)
	if err != nil {
		return nil, fmt.Errorf("ключи %s: %w", dictionary, err)
	}
	defer rows.Close()
	owners := map[string]ids.UUID{}
	for rows.Next() {
		var key, owner string
		if err := rows.Scan(&key, &owner); err != nil {
			return nil, err
		}
		owners[key] = ids.UUID(owner)
	}
	return owners, rows.Err()
}

// Candidates — нечёткие совпадения по префиксу и триграммам.
func (r *References) Candidates(
	ctx context.Context,
	dictionary, key string,
	limit int,
) ([]reference.Candidate, error) {
	rows, err := r.q.Query(ctx, selectCandidates, dictionary, key, likePrefix(key), limit)
	if err != nil {
		return nil, fmt.Errorf("кандидаты %s: %w", dictionary, err)
	}
	defer rows.Close()
	var out []reference.Candidate
	for rows.Next() {
		var (
			candidate reference.Candidate
			id        string
			status    string
			score     float32
		)
		err := rows.Scan(&id, &candidate.Canonical, &candidate.AliasKey, &status, &candidate.Prefix, &score)
		if err != nil {
			return nil, err
		}
		candidate.EntryID, candidate.Status = ids.UUID(id), reference.Status(status)
		candidate.Similarity = float64(score)
		out = append(out, candidate)
	}
	return out, rows.Err()
}

// CountStatus — записей словаря в статусе.
func (r *References) CountStatus(ctx context.Context, dictionary string, status reference.Status) (int64, error) {
	var count int64
	if err := r.q.QueryRow(ctx, countReferenceStatus, dictionary, string(status)).Scan(&count); err != nil {
		return 0, fmt.Errorf("записи %s в статусе %s: %w", dictionary, status, err)
	}
	return count, nil
}

// Save пишет изменение одной пачкой операторов: удаления записей, затем
// удалённые варианты всех записей, затем строки записей и новые варианты.
// Порядок важен: объединение переносит вариант с тем же id и ключом от одной
// записи к другой. Пачка — один круг до базы на изменение, будь в нём одна
// запись или пачка поставки.
func (r *References) Save(ctx context.Context, saved, deleted []*reference.Entry) error {
	batch := &pgx.Batch{}
	var expected []saveCheck
	for _, e := range deleted {
		origin := e.Origin()
		batch.Queue(deleteReferenceEntry, string(e.ID()), string(origin.Status), origin.Version)
		expected = append(expected, saveCheck{subject: string(e.ID()), conditional: true})
	}
	for _, e := range saved {
		for _, alias := range removedAliases(e) {
			batch.Queue(deleteReferenceAlias, string(alias.ID), string(e.ID()))
			expected = append(expected, saveCheck{subject: string(alias.ID)})
		}
	}
	for _, e := range saved {
		expected = append(expected, queueEntry(batch, e))
	}
	for _, e := range saved {
		expected = append(expected, queueAliases(batch, e)...)
	}
	if len(expected) == 0 {
		return nil
	}
	results := r.q.SendBatch(ctx, batch)
	for _, check := range expected {
		tag, err := results.Exec()
		if err == nil && check.conditional && tag.RowsAffected() == 0 {
			err = reference.ErrEntryChanged
		}
		if err != nil {
			_ = results.Close()
			return check.failure(err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("записи реестра: %w", err)
	}
	for _, e := range saved {
		e.MarkSaved()
	}
	return nil
}

// failure переводит ошибку оператора пачки в ошибку домена.
func (c saveCheck) failure(err error) error {
	switch {
	case errors.Is(err, reference.ErrEntryChanged):
		return err
	case c.key != "" && isUnique(err, constraintAliasKey):
		return reference.ErrKeyTaken.WithMessage("написание «" + c.key + "» уже принадлежит другой записи")
	case c.insert && isUnique(err, ""):
		return reference.ErrEntryChanged
	default:
		return fmt.Errorf("реестр, %s: %w", c.subject, err)
	}
}

func queueEntry(batch *pgx.Batch, e *reference.Entry) saveCheck {
	s := e.State()
	var merged any
	if s.MergedInto != "" {
		merged = string(s.MergedInto)
	}
	if e.IsNew() {
		batch.Queue(
			insertReferenceEntry,
			string(s.ID),
			s.Dictionary,
			s.CanonicalName,
			s.CanonicalKey,
			string(s.Status),
			merged,
			string(s.Source),
			string(s.Metadata),
			s.Occurrences,
			s.Revision,
			s.Version,
			s.CreatedAt,
			s.UpdatedAt,
		)
		return saveCheck{subject: string(s.ID), insert: true}
	}
	origin := e.Origin()
	batch.Queue(
		updateReferenceEntry,
		string(s.ID),
		string(origin.Status),
		origin.Version,
		s.CanonicalName,
		s.CanonicalKey,
		string(s.Status),
		merged,
		string(s.Source),
		string(s.Metadata),
		s.Occurrences,
		s.Revision,
		s.Version,
		s.UpdatedAt,
	)
	return saveCheck{subject: string(s.ID), conditional: true}
}

func queueAliases(batch *pgx.Batch, e *reference.Entry) []saveCheck {
	s := e.State()
	before := map[ids.UUID]reference.AliasState{}
	for _, alias := range e.Origin().Aliases {
		before[alias.ID] = alias
	}
	var expected []saveCheck
	for _, alias := range s.Aliases {
		old, existed := before[alias.ID]
		if existed && old == alias {
			continue
		}
		if existed {
			batch.Queue(
				updateReferenceAlias,
				string(alias.ID),
				string(s.ID),
				alias.Text,
				alias.Key,
				string(alias.Kind),
				alias.Locale,
				string(alias.Source),
				alias.Weight,
			)
		} else {
			batch.Queue(
				insertReferenceAlias,
				string(alias.ID),
				string(s.ID),
				s.Dictionary,
				alias.Text,
				alias.Key,
				string(alias.Kind),
				alias.Locale,
				string(alias.Source),
				alias.Weight,
			)
		}
		expected = append(expected, saveCheck{subject: string(alias.ID), key: alias.Key})
	}
	return expected
}

func (r *References) dictionary(ctx context.Context, query, code string) (*reference.Dictionary, error) {
	dict, err := scanDictionary(r.q.QueryRow(ctx, query, code))
	if notFound(err) {
		return nil, reference.ErrDictionaryNotFound
	}
	return dict, err
}

// entries — записи по запросу с entryColumns и их варианты вторым запросом.
func (r *References) entries(ctx context.Context, query string, args ...any) ([]*reference.Entry, error) {
	rows, err := r.q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("записи реестра: %w", err)
	}
	var states []reference.EntryState
	for rows.Next() {
		state, err := scanEntry(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		states = append(states, state)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("записи реестра: %w", err)
	}
	if len(states) == 0 {
		return nil, nil
	}
	aliases, err := r.aliases(ctx, states)
	if err != nil {
		return nil, err
	}
	out := make([]*reference.Entry, 0, len(states))
	for _, state := range states {
		state.Aliases = aliases[state.ID]
		out = append(out, reference.RestoreEntry(state))
	}
	return out, nil
}

func (r *References) aliases(
	ctx context.Context,
	states []reference.EntryState,
) (map[ids.UUID][]reference.AliasState, error) {
	list := make([]string, 0, len(states))
	for _, state := range states {
		list = append(list, string(state.ID))
	}
	rows, err := r.q.Query(ctx, selectEntryAliases, list)
	if err != nil {
		return nil, fmt.Errorf("варианты реестра: %w", err)
	}
	defer rows.Close()
	out := map[ids.UUID][]reference.AliasState{}
	for rows.Next() {
		var (
			alias                   reference.AliasState
			entry, id, kind, source string
		)
		err := rows.Scan(&entry, &id, &alias.Text, &alias.Key, &kind, &alias.Locale, &source, &alias.Weight)
		if err != nil {
			return nil, err
		}
		alias.ID, alias.Kind, alias.Source = ids.UUID(id), reference.AliasKind(kind), reference.Source(source)
		out[ids.UUID(entry)] = append(out[ids.UUID(entry)], alias)
	}
	return out, rows.Err()
}

func scanDictionary(row rowScanner) (*reference.Dictionary, error) {
	var (
		state  reference.DictionaryState
		kind   string
		kinds  []string
		limits []byte
	)
	err := row.Scan(&state.Code, &kind, &state.Identity, &kinds, &limits, &state.Revision, &state.Entries)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(limits, &state.Limits); err != nil {
		return nil, fmt.Errorf("лимиты словаря %s: %w", state.Code, err)
	}
	state.Kind = reference.Kind(kind)
	for _, k := range kinds {
		state.AliasKinds = append(state.AliasKinds, reference.AliasKind(k))
	}
	return reference.RestoreDictionary(state), nil
}

func scanEntry(row rowScanner) (reference.EntryState, error) {
	var (
		state          reference.EntryState
		id, status     string
		source         string
		merged         *string
		metadata       []byte
		dictionaryCode string
	)
	err := row.Scan(
		&id,
		&dictionaryCode,
		&state.CanonicalName,
		&state.CanonicalKey,
		&status,
		&merged,
		&source,
		&metadata,
		&state.Occurrences,
		&state.Revision,
		&state.Version,
		&state.CreatedAt,
		&state.UpdatedAt,
	)
	if err != nil {
		return state, fmt.Errorf("запись реестра: %w", err)
	}
	state.ID, state.Dictionary = ids.UUID(id), dictionaryCode
	state.Status, state.Source = reference.Status(status), reference.Source(source)
	state.Metadata = metadata
	if merged != nil {
		state.MergedInto = ids.UUID(*merged)
	}
	state.CreatedAt, state.UpdatedAt = state.CreatedAt.UTC(), state.UpdatedAt.UTC()
	return state, nil
}

func removedAliases(e *reference.Entry) []reference.AliasState {
	kept := map[ids.UUID]bool{}
	for _, alias := range e.State().Aliases {
		kept[alias.ID] = true
	}
	var removed []reference.AliasState
	for _, alias := range e.Origin().Aliases {
		if !kept[alias.ID] {
			removed = append(removed, alias)
		}
	}
	return removed
}

func uuidStrings(list []ids.UUID) []string {
	out := make([]string, 0, len(list))
	for _, id := range list {
		out = append(out, string(id))
	}
	return out
}

// likePrefix — шаблон LIKE «начинается с ключа»: спецсимволы ключа экранируются.
func likePrefix(key string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(key) + "%"
}
