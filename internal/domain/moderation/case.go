package moderation

import (
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CaseState снимок дела для хранения и проверки версии решения.
type CaseState struct {
	Card      *ReviewCard   `json:"card,omitempty"`
	ID        ids.UUID      `json:"id"`
	TenantID  ids.TenantID  `json:"tenantId"`
	ProductID ids.ProductID `json:"productId"`
	Revision  int64         `json:"revision"`
	Source    string        `json:"source"`
	Status    Status        `json:"status"`
	Reasons   []Reason      `json:"reasons"`
	Note      string        `json:"note"`
	OpenedAt  time.Time     `json:"openedAt"`
	DecidedAt *time.Time    `json:"decidedAt"`
	Version   int64         `json:"version"`
}

// Case сохраняет ревизию проверки, чтобы оператор не одобрил изменённую карточку.
type Case struct {
	state  CaseState
	origin CaseState
}

// CaseConflict возвращает актуальное дело для повторного решения оператора.
type CaseConflict struct{ Current CaseState }

// Error сохраняет стабильное сообщение конфликта.
func (e *CaseConflict) Error() string { return ErrCaseConflict.Error() }

// Unwrap сохраняет классификацию через errors.Is.
func (e *CaseConflict) Unwrap() error { return ErrCaseConflict }

// NewCase фиксирует причины на момент открытия, независимо от будущих правил.
func NewCase(id ids.UUID, verdict VerdictState, source, note string, now time.Time, card *ReviewCard) *Case {
	return &Case{state: CaseState{
		Card:      cloneCard(card),
		ID:        id,
		TenantID:  verdict.TenantID,
		ProductID: verdict.ProductID,
		Revision:  verdict.Revision,
		Source:    source,
		Status:    Open,
		Reasons:   slices.Clone(verdict.Reasons),
		Note:      strings.TrimSpace(note),
		OpenedAt:  now,
		Version:   1,
	}}
}

// RestoreCase восстанавливает исходную версию для условного обновления.
func RestoreCase(s CaseState) *Case {
	s.Card = cloneCard(s.Card)
	s.Reasons = slices.Clone(s.Reasons)
	if s.DecidedAt != nil {
		at := *s.DecidedAt
		s.DecidedAt = &at
	}
	return &Case{state: s, origin: s}
}

// State отделяет снимок от изменяемого состояния агрегата.
func (c *Case) State() CaseState {
	s := c.state
	s.Card = cloneCard(s.Card)
	s.Reasons = slices.Clone(s.Reasons)
	if s.DecidedAt != nil {
		t := *s.DecidedAt
		s.DecidedAt = &t
	}
	return s
}

// Origin сохраняет прочитанную версию для обнаружения конкурирующей записи.
func (c *Case) Origin() CaseState { return RestoreCase(c.origin).State() }

// MarkSaved начинает новый цикл изменений после успешного сохранения.
func (c *Case) MarkSaved() { c.origin = c.State() }

// Refresh обновляет открытое дело или закрывает его при исчезновении оснований.
func (c *Case) Refresh(v VerdictState, now time.Time, card *ReviewCard) bool {
	if c.state.Status != Open {
		return false
	}
	if v.Status != Pending {
		c.state.Status = Superseded
		c.state.DecidedAt = &now
	} else if c.state.Revision == v.Revision && reflect.DeepEqual(c.state.Reasons, v.Reasons) {
		return false
	}
	if c.state.Revision != v.Revision {
		c.state.Card = cloneCard(card)
	}
	c.state.Revision = v.Revision
	c.state.Reasons = slices.Clone(v.Reasons)
	c.state.Version = c.origin.Version + 1
	return true
}

// Decide фиксирует решение для текущей ревизии.
func (c *Case) Decide(expected, revision int64, action Action, note string, now time.Time) (bool, error) {
	if expected < 1 || expected != c.state.Version || c.state.Status != Open {
		return false, &CaseConflict{Current: c.State()}
	}
	if revision != c.state.Revision {
		return false, ErrCaseStale
	}
	if action != Allow && action != Reject {
		return false, ErrInvalid
	}
	note = strings.TrimSpace(note)
	if note == "" || utf8.RuneCountInString(note) > 2000 {
		return false, ErrInvalid
	}
	c.state.Status = Allowed
	if action == Reject {
		c.state.Status = Rejected
	}
	c.state.Note = note
	c.state.DecidedAt = &now
	c.state.Version = c.origin.Version + 1
	return true, nil
}
