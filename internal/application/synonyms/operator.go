package synonyms

import (
	"context"
	"slices"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/synonyms"
)

// Disable сохраняет ручное решение и при необходимости сразу меняет снимок.
func (s *Service) Disable(ctx context.Context, id string, change OperatorChange) (int64, error) {
	if !change.valid() {
		return 0, synonyms.ErrInvalid
	}
	var revision int64
	err := s.transact(ctx, true, func(ctx context.Context, m *session) error {
		if m.head.Revision != change.ExpectedRevision {
			return synonyms.ErrConflict
		}
		if err := m.disableByID(ctx, id, change); err != nil {
			return err
		}
		revision = m.head.Revision
		return nil
	})
	return revision, err
}

func (m *session) disableByID(ctx context.Context, id string, change OperatorChange) error {
	entry, err := m.repo.Find(ctx, id)
	if err != nil || entry.Phase == synonyms.Disabled {
		return err
	}
	if err := m.disable(ctx, entry, change.Actor, strings.TrimSpace(change.Reason)); err != nil {
		return err
	}
	if entry.Phase != synonyms.Active {
		return nil
	}
	active, err := m.repo.Active(ctx)
	if err != nil {
		return err
	}
	return m.publish(ctx, ruleList(active))
}

// ReplaceStopList атомарно применяет новые запреты ко всему опубликованному словарю.
func (s *Service) ReplaceStopList(ctx context.Context, terms []string, change OperatorChange) (StopList, error) {
	blocked, err := synonyms.NormalizeBlocked(terms)
	if err != nil || !change.valid() || terms == nil {
		return StopList{}, synonyms.ErrInvalid
	}
	var result StopList
	err = s.transact(ctx, true, func(ctx context.Context, m *session) error {
		if m.head.Revision != change.ExpectedRevision {
			return synonyms.ErrConflict
		}
		if !slices.Equal(m.head.BlockedTerms, blocked) {
			if err := m.replaceStopList(ctx, blocked, change); err != nil {
				return err
			}
		}
		result = m.stopList()
		return nil
	})
	return result, err
}

func (m *session) replaceStopList(ctx context.Context, blocked []string, change OperatorChange) error {
	active, err := m.repo.Active(ctx)
	if err != nil {
		return err
	}
	m.head.BlockedTerms = blocked
	keys := keySet(blocked)
	rules := make([]synonyms.Rule, 0, len(active))
	for _, entry := range active {
		if !entry.Blocked(keys) {
			rules = append(rules, entry.Rule)
			continue
		}
		if err := m.disable(ctx, entry, change.Actor, "stop_list: "+strings.TrimSpace(change.Reason)); err != nil {
			return err
		}
	}
	event := synonyms.Transition{At: m.now, Actor: change.Actor, Reason: strings.TrimSpace(change.Reason)}
	if err := m.repo.RecordTransition(ctx, "stop_list", event); err != nil {
		return err
	}
	return m.publish(ctx, rules)
}

func (c OperatorChange) valid() bool {
	reason := strings.TrimSpace(c.Reason)
	return c.ExpectedRevision >= 0 && c.Actor != "" && reason != "" &&
		reference.Length(reason) <= 256 && !strings.ContainsRune(reason, 0)
}

func keySet(terms []string) map[string]bool {
	set := make(map[string]bool, len(terms))
	for _, term := range terms {
		set[term] = true
	}
	return set
}
