package postgres

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/synonyms"
)

const (
	synonymHeadColumns = `revision, last_published, budget_day, checked, model_calls, model_input,
		to_json(blocked_terms)`
	readSynonymHead     = `SELECT ` + synonymHeadColumns + ` FROM reference.search_synonym_head WHERE singleton`
	lockSynonymHead     = readSynonymHead + ` FOR UPDATE`
	readSynonymSnapshot = `SELECT revision, CASE WHEN revision = $1 THEN '' ELSE snapshot END
		FROM reference.search_synonym_head WHERE singleton`
	saveSynonymHead = `UPDATE reference.search_synonym_head
		SET revision = $1,
			last_published = $2,
			budget_day = $3,
			checked = $4,
			model_calls = $5,
			model_input = $6,
			blocked_terms = $7,
			snapshot = COALESCE(NULLIF($8, ''), snapshot)
		WHERE singleton`
	insertSynonym = `INSERT INTO reference.search_synonym_candidates
		(id, phase, version, expires_at, next_check, body) VALUES ($1, $2, $3, $4, $5, $6)`
	saveSynonym = `UPDATE reference.search_synonym_candidates
		SET phase = $2,
			version = $3,
			expires_at = $4,
			next_check = $5,
			passed_at = $6,
			body = $7
		WHERE id = $1 AND phase = $8 AND version = $9`
	insertSynonymRule = `INSERT INTO reference.search_synonym_rules (id, category_id, from_term, to_term)
		VALUES ($1, $2, $3, $4)`
	deleteSynonymRule       = `DELETE FROM reference.search_synonym_rules WHERE id = $1`
	insertSynonymTransition = `INSERT INTO reference.search_synonym_transitions
		(candidate_id, from_phase, to_phase, at, actor, reason) VALUES ($1, $2, $3, $4, $5, $6)`
	insertSynonymCheck = `INSERT INTO reference.search_synonym_checks
		(batch_id, candidate_id, recorded_at, body) VALUES ($1, $2, $3, $4)`
	findSynonym    = `SELECT body FROM reference.search_synonym_candidates WHERE id = $1`
	knownSynonyms  = `SELECT body FROM reference.search_synonym_candidates WHERE id = ANY($1)`
	activeSynonyms = `SELECT c.body FROM reference.search_synonym_rules r
		JOIN reference.search_synonym_candidates c ON c.id = r.id ORDER BY r.id`
	dueSynonyms = `SELECT body FROM reference.search_synonym_candidates
		WHERE id = ANY($3) AND next_check <= $1 AND
			(phase = 'active' OR (phase = 'quarantined' AND expires_at > $1 AND passed_at IS NULL))
		ORDER BY phase, next_check, id LIMIT $2`
	readySynonyms = `SELECT body FROM reference.search_synonym_candidates
		WHERE phase = 'quarantined' AND passed_at IS NOT NULL AND expires_at > $1 AND id = ANY($3)
		ORDER BY passed_at, id LIMIT $2`
	countLiveSynonyms = `SELECT count(*) FROM reference.search_synonym_candidates
		WHERE phase IN ('candidate', 'quarantined') AND expires_at > $1`
	listSynonyms = `SELECT body FROM reference.search_synonym_candidates
		WHERE id > $1 AND (phase IN ('active', 'disabled') OR expires_at > $2)
		ORDER BY id LIMIT $3`
)

type searchSynonyms struct {
	db *gorm.DB
}

func (s *scope) Synonyms() ports.SynonymRepository {
	return &searchSynonyms{db: s.db}
}

func (r *searchSynonyms) Current(ctx context.Context, lock bool) (ports.SynonymHead, error) {
	query := readSynonymHead
	if lock {
		query = lockSynonymHead
	}
	var head ports.SynonymHead
	var terms []byte
	err := rawSQL(ctx, r.db, query).Row().Scan(
		&head.Revision,
		&head.LastPublished,
		&head.BudgetDay,
		&head.Checked,
		&head.ModelCalls,
		&head.ModelInput,
		&terms,
	)
	if err == nil {
		err = json.Unmarshal(terms, &head.BlockedTerms)
	}
	return head, err
}

func (r *searchSynonyms) Snapshot(ctx context.Context, known int64) (int64, []byte, error) {
	var revision int64
	var body []byte
	err := rawSQL(ctx, r.db, readSynonymSnapshot, known).Row().Scan(&revision, &body)
	return revision, body, err
}

func (r *searchSynonyms) SaveHead(ctx context.Context, h ports.SynonymHead, body []byte) error {
	_, err := execSQL(
		ctx,
		r.db,
		saveSynonymHead,
		h.Revision,
		h.LastPublished,
		h.BudgetDay,
		h.Checked,
		h.ModelCalls,
		h.ModelInput,
		h.BlockedTerms,
		string(body),
	)
	return err
}

func (r *searchSynonyms) Find(ctx context.Context, id string) (synonyms.Entry, error) {
	var raw []byte
	err := rawSQL(ctx, r.db, findSynonym, id).Row().Scan(&raw)
	if notFound(err) {
		return synonyms.Entry{}, synonyms.ErrNotFound
	}
	var entry synonyms.Entry
	if err == nil {
		err = json.Unmarshal(raw, &entry)
	}
	return entry, err
}

func (r *searchSynonyms) Known(ctx context.Context, ids []string) ([]synonyms.Entry, error) {
	return r.entries(ctx, knownSynonyms, ids)
}

func (r *searchSynonyms) Active(ctx context.Context) ([]synonyms.Entry, error) {
	return r.entries(ctx, activeSynonyms)
}

func (r *searchSynonyms) Due(ctx context.Context, now time.Time, limit int, ids []string) ([]synonyms.Entry, error) {
	return r.entries(ctx, dueSynonyms, now, limit, ids)
}

func (r *searchSynonyms) Ready(ctx context.Context, now time.Time, limit int, ids []string) ([]synonyms.Entry, error) {
	return r.entries(ctx, readySynonyms, now, limit, ids)
}

func (r *searchSynonyms) Entries(
	ctx context.Context,
	now time.Time,
	after string,
	limit int,
) ([]synonyms.Entry, error) {
	return r.entries(ctx, listSynonyms, after, now, limit)
}

func (r *searchSynonyms) LiveCount(ctx context.Context, now time.Time) (int, error) {
	var count int
	err := rawSQL(ctx, r.db, countLiveSynonyms, now).Row().Scan(&count)
	return count, err
}

func (r *searchSynonyms) entries(ctx context.Context, query string, args ...any) ([]synonyms.Entry, error) {
	rows, err := rawSQL(ctx, r.db, query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	entries := make([]synonyms.Entry, 0)
	for rows.Next() {
		var raw []byte
		var entry synonyms.Entry
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (r *searchSynonyms) Insert(ctx context.Context, e synonyms.Entry) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = execSQL(ctx, r.db, insertSynonym, e.ID, e.Phase, e.Version, e.ExpiresAt, e.NextCheck, string(raw))
	return err
}

func (r *searchSynonyms) Save(ctx context.Context, prior, e synonyms.Entry) error {
	if e.ID != prior.ID || e.Version != prior.Version+1 {
		return synonyms.ErrConflict
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var passedAt *time.Time
	if e.LastCheck != nil && e.LastCheck.Outcome == synonyms.Pass {
		passedAt = &e.LastCheck.RecordedAt
	}
	n, err := execSQL(
		ctx,
		r.db,
		saveSynonym,
		e.ID,
		e.Phase,
		e.Version,
		e.ExpiresAt,
		e.NextCheck,
		passedAt,
		string(raw),
		prior.Phase,
		prior.Version,
	)
	if err != nil {
		return err
	}
	if n != 1 {
		return synonyms.ErrConflict
	}
	return r.syncRule(ctx, prior.Phase, e)
}

func (r *searchSynonyms) syncRule(ctx context.Context, prior synonyms.Phase, e synonyms.Entry) error {
	switch {
	case prior != synonyms.Active && e.Phase == synonyms.Active:
		_, err := execSQL(ctx, r.db, insertSynonymRule, e.ID, e.Scope.ID, e.From, e.To)
		return err
	case prior == synonyms.Active && e.Phase != synonyms.Active:
		_, err := execSQL(ctx, r.db, deleteSynonymRule, e.ID)
		return err
	default:
		return nil
	}
}

func (r *searchSynonyms) RecordTransition(ctx context.Context, id string, event synonyms.Transition) error {
	_, err := execSQL(
		ctx,
		r.db,
		insertSynonymTransition,
		id,
		event.From,
		event.To,
		event.At,
		event.Actor,
		event.Reason,
	)
	return err
}

func (r *searchSynonyms) RecordCheck(ctx context.Context, check synonyms.Check) error {
	raw, err := json.Marshal(check)
	if err != nil {
		return err
	}
	_, err = execSQL(ctx, r.db, insertSynonymCheck, check.BatchID, check.ID, check.RecordedAt, string(raw))
	return err
}
