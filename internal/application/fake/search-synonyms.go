package fake

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/synonyms"
)

type synonymData struct {
	head    ports.SynonymHead
	body    []byte
	inputs  []ports.SynonymInput
	brands  []string
	entries map[string]synonyms.Entry
	checks  []synonyms.Check
	journal []synonyms.Transition
}

// SynonymValidator позволяет сценариям задать ответ поиска без сетевого вызова.
type SynonymValidator struct {
	ValidateFunc func(context.Context, ports.SynonymValidationRequest) (ports.SynonymValidationResponse, error)
}

// Validate вызывает обработчик, заданный тестом.
func (f SynonymValidator) Validate(
	ctx context.Context,
	request ports.SynonymValidationRequest,
) (ports.SynonymValidationResponse, error) {
	return f.ValidateFunc(ctx, request)
}

func newSynonymData() *synonymData {
	return &synonymData{
		head:    ports.SynonymHead{BlockedTerms: []string{}},
		body:    []byte(`{"schemaVersion":1,"revision":0,"rules":[],"blockedTerms":[]}`),
		entries: make(map[string]synonyms.Entry),
	}
}

func (d *synonymData) clone() *synonymData {
	if d == nil {
		return nil
	}
	copy := *d
	copy.entries = maps.Clone(d.entries)
	copy.checks = slices.Clone(d.checks)
	copy.journal = slices.Clone(d.journal)
	return &copy
}

func (d *data) synonymState() *synonymData {
	if d.synonyms == nil {
		d.synonyms = newSynonymData()
	}
	return d.synonyms
}

func (t *tx) Synonyms() ports.SynonymRepository { return t.data.synonymState() }

// SeedSynonymSources заменяет доверенные входы и известные ключи брендов.
func (s *Store) SeedSynonymSources(inputs []ports.SynonymInput, brands ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.data.synonymState()
	state.inputs = slices.Clone(inputs)
	state.brands = slices.Clone(brands)
}

func (d *synonymData) Current(context.Context, bool) (ports.SynonymHead, error) {
	return d.head, nil
}

func (d *synonymData) Snapshot(_ context.Context, known int64) (int64, []byte, error) {
	if known == d.head.Revision {
		return d.head.Revision, nil, nil
	}
	return d.head.Revision, slices.Clone(d.body), nil
}

func (d *synonymData) SaveHead(_ context.Context, head ports.SynonymHead, body []byte) error {
	d.head = head
	if body != nil {
		d.body = body
	}
	return nil
}

func (d *synonymData) Inputs(context.Context) ([]ports.SynonymInput, error) {
	return d.inputs, nil
}

func (d *synonymData) Brands(context.Context, []string) ([]string, error) {
	return slices.Clone(d.brands), nil
}

func (d *synonymData) Find(_ context.Context, id string) (synonyms.Entry, error) {
	item, exists := d.entries[id]
	if !exists {
		return synonyms.Entry{}, synonyms.ErrNotFound
	}
	return item, nil
}

func (d *synonymData) Known(_ context.Context, ids []string) ([]synonyms.Entry, error) {
	items := make([]synonyms.Entry, 0, len(ids))
	for _, id := range ids {
		if item, exists := d.entries[id]; exists {
			items = append(items, item)
		}
	}
	return items, nil
}

func (d *synonymData) Entries(_ context.Context, now time.Time, after string, limit int) ([]synonyms.Entry, error) {
	items := d.match(func(item synonyms.Entry) bool {
		return item.ID > after && (item.Phase == synonyms.Disabled || item.Phase == synonyms.Active ||
			item.ExpiresAt.After(now))
	})
	return items[:min(limit, len(items))], nil
}

func (d *synonymData) Active(context.Context) ([]synonyms.Entry, error) {
	return d.match(func(item synonyms.Entry) bool { return item.Phase == synonyms.Active }), nil
}

func (d *synonymData) Due(_ context.Context, now time.Time, limit int, ids []string) ([]synonyms.Entry, error) {
	items := d.match(func(item synonyms.Entry) bool {
		passed := item.LastCheck != nil && item.LastCheck.Outcome == synonyms.Pass
		quarantined := item.Phase == synonyms.Quarantined && item.ExpiresAt.After(now) && !passed
		eligible := item.Phase == synonyms.Active || quarantined
		return slices.Contains(ids, item.ID) && !item.NextCheck.After(now) && eligible
	})
	slices.SortStableFunc(items, func(a, b synonyms.Entry) int {
		if a.Phase != b.Phase {
			return strings.Compare(string(a.Phase), string(b.Phase))
		}
		return a.NextCheck.Compare(b.NextCheck)
	})
	return items[:min(limit, len(items))], nil
}

func (d *synonymData) Ready(_ context.Context, now time.Time, limit int, ids []string) ([]synonyms.Entry, error) {
	items := d.match(func(item synonyms.Entry) bool {
		return slices.Contains(ids, item.ID) && item.Phase == synonyms.Quarantined && item.ExpiresAt.After(now) &&
			item.LastCheck != nil && item.LastCheck.Outcome == synonyms.Pass
	})
	slices.SortStableFunc(items, func(a, b synonyms.Entry) int {
		return a.LastCheck.RecordedAt.Compare(b.LastCheck.RecordedAt)
	})
	return items[:min(limit, len(items))], nil
}

func (d *synonymData) LiveCount(_ context.Context, now time.Time) (int, error) {
	return len(d.match(func(item synonyms.Entry) bool {
		return (item.Phase == synonyms.Candidate || item.Phase == synonyms.Quarantined) && item.ExpiresAt.After(now)
	})), nil
}

func (d *synonymData) match(keep func(synonyms.Entry) bool) []synonyms.Entry {
	items := make([]synonyms.Entry, 0)
	for _, item := range d.entries {
		if keep(item) {
			items = append(items, item)
		}
	}
	slices.SortFunc(items, func(a, b synonyms.Entry) int { return strings.Compare(a.ID, b.ID) })
	return items
}

func (d *synonymData) Insert(_ context.Context, item synonyms.Entry) error {
	d.entries[item.ID] = item
	return nil
}

func (d *synonymData) Save(_ context.Context, prior, next synonyms.Entry) error {
	item := d.entries[prior.ID]
	if item.Version != prior.Version || item.Phase != prior.Phase {
		return synonyms.ErrConflict
	}
	d.entries[next.ID] = next
	return nil
}

func (d *synonymData) RecordCheck(_ context.Context, check synonyms.Check) error {
	d.checks = append(d.checks, check)
	return nil
}

func (d *synonymData) RecordTransition(_ context.Context, _ string, event synonyms.Transition) error {
	d.journal = append(d.journal, event)
	return nil
}
