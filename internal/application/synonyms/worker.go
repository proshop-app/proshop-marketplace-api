package synonyms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/synonyms"
)

// Кодирование JSON может занимать до шести байт на руну термина.
const maxBlockedBytes = 2 + synonyms.MaxBlocked*(6*synonyms.MaxTermLength+3)

// RunResult содержит агрегаты для фоновых метрик без терминов и идентификаторов.
type RunResult struct {
	Pass         int
	Reject       int
	Retry        int
	Activated    int
	Active       int
	Queued       int
	DailyChecked int
	LimitReached bool
}

type batch struct {
	request ports.SynonymValidationRequest
	entries []synonyms.Entry
}

// Run сверяет источники, проверяет пары вне транзакции и публикует готовую пачку.
func (s *Service) Run(ctx context.Context) (RunResult, error) {
	var data sources
	var summary RunResult
	err := s.transact(ctx, true, func(ctx context.Context, m *session) error {
		var err error
		data, err = m.sources(ctx)
		if err != nil {
			return err
		}
		active, changed, err := m.prune(ctx, data)
		if err != nil {
			return err
		}
		if changed {
			if err := m.publish(ctx, ruleList(active)); err != nil {
				return err
			}
		}
		return m.collect(ctx, data)
	})
	if err != nil {
		return summary, err
	}
	summary.LimitReached = data.overflow
	var checkErr error
	if s.validator != nil {
		checkErr = s.check(ctx, data.order, &summary)
	}
	publishErr := s.finalize(ctx, &summary)
	modelErr := s.runModel(ctx, data.taxonomy)
	return summary, errors.Join(checkErr, publishErr, modelErr)
}

func (s *Service) check(ctx context.Context, eligible []string, summary *RunResult) error {
	for requested := 0; requested < synonyms.DailyChecks; {
		var job batch
		err := s.transact(ctx, true, func(ctx context.Context, m *session) error {
			var err error
			job, err = m.reserve(ctx, eligible)
			return err
		})
		if err != nil || len(job.entries) == 0 {
			return err
		}
		requested += len(job.entries)
		response, validationErr := s.validator.Validate(ctx, job.request)
		byID, err := matchResults(job.request, response, validationErr)
		saveErr := s.record(ctx, job, byID, summary)
		if err != nil || saveErr != nil {
			return errors.Join(err, saveErr)
		}
	}
	return nil
}

func (m *session) reserve(ctx context.Context, eligible []string) (batch, error) {
	m.rollBudget()
	limit := min(synonyms.MaxBatch, synonyms.DailyChecks-m.head.Checked)
	if limit <= 0 || len(eligible) == 0 {
		return batch{}, nil
	}
	entries, err := m.repo.Due(ctx, m.now, limit, eligible)
	if err != nil || len(entries) == 0 {
		return batch{}, err
	}
	id := fmt.Sprintf("syn-%s-%03d", m.head.BudgetDay, m.head.Checked+1)
	directions := make([]ports.SynonymValidationRule, 0, len(entries))
	for i, entry := range entries {
		before := entry
		entry.Reserve(m.now)
		if err := m.repo.Save(ctx, before, entry); err != nil {
			return batch{}, err
		}
		entries[i] = entry
		rule := ports.SynonymValidationRule{Direction: entry.Direction}
		if entry.Source.Kind == synonyms.RegistryAlias {
			rule.Source = "reference_alias"
		}
		directions = append(directions, rule)
	}
	m.head.Checked += len(entries)
	job := batch{
		request: ports.SynonymValidationRequest{BatchID: id, Rules: directions},
		entries: entries,
	}
	return job, m.repo.SaveHead(ctx, m.head, nil)
}

func (m *session) rollBudget() {
	day := m.now.Format(time.DateOnly)
	if m.head.BudgetDay < day {
		m.head.BudgetDay = day
		m.head.Checked = 0
		m.head.ModelCalls = 0
	}
}

func matchResults(
	request ports.SynonymValidationRequest,
	response ports.SynonymValidationResponse,
	cause error,
) (map[string]synonyms.Validation, error) {
	if cause != nil {
		return nil, cause
	}
	mismatch := synonyms.ErrInvalid.WithMessage("ответ валидатора поискового словаря не соответствует пачке")
	if response.BatchID != request.BatchID || response.ValidatorVersion != synonyms.ValidatorVersion ||
		len(response.Results) != len(request.Rules) {
		return nil, mismatch
	}
	expected := make(map[string]bool, len(request.Rules))
	for _, rule := range request.Rules {
		expected[rule.ID] = true
	}
	byID := make(map[string]synonyms.Validation, len(response.Results))
	for _, result := range response.Results {
		if !expected[result.ID] || !result.Valid() {
			return nil, mismatch
		}
		delete(expected, result.ID)
		byID[result.ID] = result
	}
	return byID, nil
}

func (s *Service) record(
	ctx context.Context,
	job batch,
	results map[string]synonyms.Validation,
	summary *RunResult,
) error {
	var recorded RunResult
	err := s.transact(ctx, true, func(ctx context.Context, m *session) error {
		d, err := m.tx.Taxonomy().Current(ctx)
		if err != nil {
			return err
		}
		revision := strconv.FormatInt(d.Version, 10)
		disabled := false
		for _, entry := range job.entries {
			removed, err := m.recordOne(ctx, job.request.BatchID, entry, results[entry.ID], revision, &recorded)
			if err != nil {
				return err
			}
			disabled = disabled || removed
		}
		if !disabled {
			return nil
		}
		active, err := m.repo.Active(ctx)
		if err != nil {
			return err
		}
		return m.publish(ctx, ruleList(active))
	})
	if err == nil {
		summary.Pass += recorded.Pass
		summary.Reject += recorded.Reject
		summary.Retry += recorded.Retry
	}
	return err
}

func (m *session) recordOne(
	ctx context.Context,
	batchID string,
	reserved synonyms.Entry,
	result synonyms.Validation,
	revision string,
	summary *RunResult,
) (bool, error) {
	version := synonyms.ValidatorVersion
	if result.ID == "" {
		result = synonyms.Validation{ID: reserved.ID, Decision: synonyms.Retry}
		version = 0
	}
	outcome := result.Outcome(revision, m.now, reserved.Source.Kind == synonyms.RegistryAlias)
	check := synonyms.Check{
		Validation:       result,
		BatchID:          batchID,
		ValidatorVersion: version,
		RecordedAt:       m.now,
		Outcome:          outcome,
	}
	if err := m.repo.RecordCheck(ctx, check); err != nil {
		return false, err
	}
	current, err := m.repo.Find(ctx, reserved.ID)
	if err != nil || current.Version != reserved.Version {
		return false, err
	}
	return m.applyCheck(ctx, current, check, summary)
}

func (m *session) applyCheck(
	ctx context.Context,
	entry synonyms.Entry,
	check synonyms.Check,
	summary *RunResult,
) (bool, error) {
	before := entry
	switch check.Outcome {
	case synonyms.Pass:
		summary.Pass++
	case synonyms.Reject:
		summary.Reject++
	default:
		summary.Retry++
	}
	if check.Outcome == synonyms.Reject && entry.Phase == synonyms.Active {
		move, err := entry.RejectActive(check, m.now)
		if err != nil {
			return false, err
		}
		return true, m.transition(ctx, before, entry, move)
	}
	entry.Record(check, m.now)
	return false, m.repo.Save(ctx, before, entry)
}

func (s *Service) finalize(ctx context.Context, summary *RunResult) error {
	result := *summary
	err := s.transact(ctx, true, func(ctx context.Context, m *session) error {
		data, err := m.sources(ctx)
		if err != nil {
			return err
		}
		result.LimitReached = result.LimitReached || data.overflow
		active, removed, err := m.prune(ctx, data)
		if err != nil {
			return err
		}
		result.DailyChecked = m.head.Checked
		result.Queued, err = m.repo.LiveCount(ctx, m.now)
		if err != nil {
			return err
		}
		return m.publishReady(ctx, data, ruleList(active), removed, &result)
	})
	if err == nil {
		*summary = result
	}
	return err
}

func (m *session) publishReady(
	ctx context.Context,
	data sources,
	rules []synonyms.Rule,
	removed bool,
	summary *RunResult,
) error {
	day := m.now.Format(time.DateOnly)
	limit := min(synonyms.DailyActivations, synonyms.MaxActive-len(rules))
	if limit > 0 && m.head.LastPublished < day && len(data.order) > 0 {
		var err error
		rules, err = m.activate(ctx, data, rules, limit, summary)
		if err != nil {
			return err
		}
	}
	summary.Active = len(rules)
	summary.LimitReached = summary.LimitReached || len(rules) == synonyms.MaxActive ||
		summary.Queued >= synonyms.MaxCandidates
	if summary.Activated > 0 {
		m.head.LastPublished = day
		summary.Queued -= summary.Activated
	}
	if summary.Activated == 0 && !removed {
		return nil
	}
	slices.SortFunc(rules, func(a, b synonyms.Rule) int { return strings.Compare(a.ID, b.ID) })
	return m.publish(ctx, rules)
}

func (m *session) activate(
	ctx context.Context,
	data sources,
	rules []synonyms.Rule,
	limit int,
	summary *RunResult,
) ([]synonyms.Rule, error) {
	ready, err := m.repo.Ready(ctx, m.now, limit, data.order)
	if err != nil {
		return nil, err
	}
	body, err := m.snapshot(rules)
	if err != nil {
		return nil, err
	}
	blocked, err := json.Marshal(m.head.BlockedTerms)
	if err != nil {
		return nil, err
	}
	// Оператор должен иметь возможность добавить полный стоп-лист при любом наполнении словаря.
	size := len(body) + maxBlockedBytes - len(blocked)
	revision := strconv.FormatInt(data.taxonomy.Version, 10)
	for _, entry := range ready {
		if entry.Source != data.entries[entry.ID].Source || entry.LastCheck.Evidence.TaxonomyRevision != revision {
			continue
		}
		before := entry
		move, err := entry.Activate(m.now)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(entry.Rule)
		if err != nil {
			return nil, err
		}
		nextSize := size + len(encoded)
		if len(rules) > 0 {
			nextSize++
		}
		if nextSize > synonyms.MaxSnapshot {
			summary.LimitReached = true
			continue
		}
		if err := m.transition(ctx, before, entry, move); err != nil {
			return nil, err
		}
		size = nextSize
		rules = append(rules, entry.Rule)
		summary.Activated++
	}
	return rules, nil
}
