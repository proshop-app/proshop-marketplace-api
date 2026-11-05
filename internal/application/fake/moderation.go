package fake

import (
	"context"
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type moderationData struct {
	cached               map[string]moderation.CachedDecision
	verdicts             map[productKey]moderation.VerdictState
	cases                map[ids.UUID]moderation.CaseState
	decisions            []moderation.Decision
	participantDecisions []participant.DecisionRecord
	phrases              map[string]moderation.StopPhrase
	reasons              map[string]moderation.ReasonCode
	revision             int64
}

type moderationVerdicts struct{ d *moderationData }
type moderationCases struct{ d *moderationData }
type moderationDecisions struct{ d *moderationData }
type moderationRules struct{ d *moderationData }
type participantDecisions struct{ d *moderationData }

func newModerationData() *moderationData {
	return &moderationData{
		cached:   map[string]moderation.CachedDecision{},
		verdicts: map[productKey]moderation.VerdictState{},
		cases:    map[ids.UUID]moderation.CaseState{},
		phrases:  map[string]moderation.StopPhrase{},
		reasons:  map[string]moderation.ReasonCode{},
		revision: 1,
	}
}

func (d *moderationData) clone() *moderationData {
	return &moderationData{
		cached:               maps.Clone(d.cached),
		verdicts:             maps.Clone(d.verdicts),
		cases:                maps.Clone(d.cases),
		decisions:            slices.Clone(d.decisions),
		participantDecisions: slices.Clone(d.participantDecisions),
		phrases:              maps.Clone(d.phrases),
		reasons:              maps.Clone(d.reasons),
		revision:             d.revision,
	}
}

func (t *tx) ModerationVerdicts() moderation.VerdictRepository {
	return moderationVerdicts{t.data.moderation}
}
func (t *tx) ModerationCases() moderation.CaseRepository { return moderationCases{t.data.moderation} }
func (t *tx) ModerationDecisions() moderation.DecisionRepository {
	return moderationDecisions{t.data.moderation}
}
func (t *tx) ModerationRules() moderation.RulesRepository { return moderationRules{t.data.moderation} }
func (t *tx) ParticipantDecisions() participant.DecisionRepository {
	return participantDecisions{t.data.moderation}
}

func (r moderationVerdicts) Get(_ context.Context, t ids.TenantID, p ids.ProductID) (*moderation.Verdict, error) {
	state, ok := r.d.verdicts[productKey{t, p}]
	if !ok {
		return nil, moderation.ErrNotFound
	}
	return moderation.RestoreVerdict(state), nil
}

func (r moderationVerdicts) Save(_ context.Context, v *moderation.Verdict) error {
	state := v.State()
	key := productKey{state.TenantID, state.ProductID}
	old := r.d.verdicts[key]
	if old.Version != v.Origin().Version || old.Status != v.Origin().Status {
		return moderation.ErrConflict
	}
	r.d.verdicts[key] = state
	v.MarkSaved()
	return nil
}

func (r moderationVerdicts) List(_ context.Context, f moderation.Filter) ([]moderation.VerdictState, int, error) {
	all := []moderation.VerdictState{}
	for _, v := range r.d.verdicts {
		if (f.Status == "" || v.Status == f.Status) && (f.TenantID == "" || v.TenantID == f.TenantID) {
			all = append(all, moderation.RestoreVerdict(v).State())
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].UpdatedAt.Equal(all[j].UpdatedAt) {
			return all[i].UpdatedAt.After(all[j].UpdatedAt)
		}
		if all[i].TenantID != all[j].TenantID {
			return all[i].TenantID < all[j].TenantID
		}
		return all[i].ProductID < all[j].ProductID
	})
	return window(all, f.Limit, f.Offset), len(all), nil
}

func window[T any](items []T, limit, offset int) []T {
	offset = min(max(offset, 0), len(items))
	return items[offset:min(offset+max(limit, 0), len(items))]
}

func (r moderationCases) Get(_ context.Context, id ids.UUID) (*moderation.Case, error) {
	state, ok := r.d.cases[id]
	if !ok {
		return nil, moderation.ErrNotFound
	}
	return moderation.RestoreCase(state), nil
}

func (r moderationCases) Open(_ context.Context, t ids.TenantID, p ids.ProductID) (*moderation.Case, error) {
	for _, c := range r.d.cases {
		if c.TenantID == t && c.ProductID == p && c.Status == moderation.Open {
			return moderation.RestoreCase(c), nil
		}
	}
	return nil, moderation.ErrNotFound
}

func (r moderationCases) Save(_ context.Context, c *moderation.Case) error {
	state := c.State()
	old := r.d.cases[state.ID]
	if old.Version != c.Origin().Version || old.Status != c.Origin().Status {
		return &moderation.CaseConflict{Current: moderation.RestoreCase(old).State()}
	}
	for id, other := range r.d.cases {
		if id != state.ID && other.TenantID == state.TenantID && other.ProductID == state.ProductID &&
			state.Status == moderation.Open && other.Status == moderation.Open {
			return &moderation.CaseConflict{Current: moderation.RestoreCase(other).State()}
		}
	}
	r.d.cases[state.ID] = state
	c.MarkSaved()
	return nil
}

func (r moderationCases) List(_ context.Context, f moderation.Filter) ([]moderation.CaseState, int, error) {
	all := []moderation.CaseState{}
	for _, c := range r.d.cases {
		if (f.Status == "" || c.Status == f.Status) && (f.TenantID == "" || c.TenantID == f.TenantID) &&
			(f.Source == "" || c.Source == f.Source) {
			all = append(all, moderation.RestoreCase(c).State())
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].OpenedAt.Equal(all[j].OpenedAt) {
			return all[i].ID > all[j].ID
		}
		return all[i].OpenedAt.After(all[j].OpenedAt)
	})
	return window(all, f.Limit, f.Offset), len(all), nil
}

func (r moderationDecisions) Add(_ context.Context, d moderation.Decision) error {
	r.d.decisions = append(r.d.decisions, d)
	return nil
}

func (r moderationDecisions) List(_ context.Context, t ids.TenantID, p ids.ProductID) ([]moderation.Decision, error) {
	result := []moderation.Decision{}
	for _, d := range r.d.decisions {
		c := r.d.cases[d.CaseID]
		if c.TenantID == t && c.ProductID == p {
			result = append(result, d)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].At.Equal(result[j].At) {
			return result[i].ID > result[j].ID
		}
		return result[i].At.After(result[j].At)
	})
	return result, nil
}

func (r moderationRules) Current(_ context.Context) (moderation.Rules, error) {
	rules := moderation.Rules{Revision: r.d.revision, Phrases: []moderation.StopPhrase{}}
	for _, phrase := range r.d.phrases {
		rules.Phrases = append(rules.Phrases, phrase)
	}
	sort.Slice(rules.Phrases, func(i, j int) bool {
		if !rules.Phrases[i].CreatedAt.Equal(rules.Phrases[j].CreatedAt) {
			return rules.Phrases[i].CreatedAt.Before(rules.Phrases[j].CreatedAt)
		}
		return rules.Phrases[i].Key < rules.Phrases[j].Key
	})
	return rules, nil
}

func (r moderationRules) AddPhrase(_ context.Context, phrase moderation.StopPhrase) error {
	if _, ok := r.d.phrases[phrase.Key]; ok {
		return moderation.ErrPhraseExists
	}
	r.d.phrases[phrase.Key] = phrase
	r.d.revision++
	return nil
}

func (r moderationRules) RemovePhrase(_ context.Context, key string) (bool, error) {
	if _, ok := r.d.phrases[key]; !ok {
		return false, nil
	}
	delete(r.d.phrases, key)
	r.d.revision++
	return true, nil
}

func (r moderationRules) ListReasons(_ context.Context) ([]moderation.ReasonCode, error) {
	result := []moderation.ReasonCode{}
	for _, reason := range r.d.reasons {
		result = append(result, reason)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Code < result[j].Code })
	return result, nil
}

func (r moderationRules) PutReason(_ context.Context, reason moderation.ReasonCode) error {
	if _, ok := r.d.reasons[reason.Code]; !ok {
		return moderation.ErrNotFound
	}
	r.d.reasons[reason.Code] = reason
	return nil
}

func (r participantDecisions) Add(_ context.Context, d participant.DecisionRecord) error {
	if d.Actor == "" {
		d.Actor = "operator"
	}
	r.d.participantDecisions = append(r.d.participantDecisions, d)
	return nil
}

func (r participantDecisions) List(
	_ context.Context,
	tenant ids.TenantID,
	page participant.Page,
) ([]participant.DecisionRecord, int, error) {
	result := []participant.DecisionRecord{}
	for _, d := range r.d.participantDecisions {
		if d.TenantID == tenant {
			result = append(result, d)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].At.Equal(result[j].At) {
			return result[i].ID > result[j].ID
		}
		return result[i].At.After(result[j].At)
	})
	return window(result, page.Limit, page.Offset), len(result), nil
}

// SeedVerdict задаёт явно оценённое предложение в тестах каталога.
func (s *Store) SeedVerdict(state moderation.VerdictState) {
	s.read(func(d *data) {
		d.moderation.verdicts[productKey{state.TenantID, state.ProductID}] = moderation.RestoreVerdict(state).State()
	})
}

func (r moderationDecisions) Cached(
	_ context.Context,
	key string,
	now time.Time,
) (moderation.CachedDecision, bool, error) {
	value, ok := r.d.cached[key]
	return value, ok && now.Before(value.ExpiresAt), nil
}
func (r moderationDecisions) Cache(_ context.Context, key string, value moderation.CachedDecision) error {
	r.d.cached[key] = value
	return nil
}
func (r moderationRules) AddReason(_ context.Context, reason moderation.ReasonCode) error {
	if _, ok := r.d.reasons[reason.Code]; ok {
		return moderation.ErrReasonExists
	}
	r.d.reasons[reason.Code] = reason
	return nil
}

func (r moderationCases) OpenStats(_ context.Context) ([]moderation.OpenStat, error) {
	tenants := map[ids.TenantID]moderation.OpenStat{}
	for _, c := range r.d.cases {
		if c.Status != moderation.Open {
			continue
		}
		stat := tenants[c.TenantID]
		stat.TenantID = c.TenantID
		stat.Count++
		if stat.Oldest.IsZero() || c.OpenedAt.Before(stat.Oldest) {
			stat.Oldest = c.OpenedAt
		}
		tenants[c.TenantID] = stat
	}
	result := make([]moderation.OpenStat, 0, len(tenants))
	for _, stat := range tenants {
		result = append(result, stat)
	}
	return result, nil
}
