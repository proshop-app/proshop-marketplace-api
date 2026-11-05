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
	ID        ids.UUID
	TenantID  ids.TenantID
	ProductID ids.ProductID
	Revision  int64
	Source    string
	Status    string
	Reasons   []Reason
	Note      string
	OpenedAt  time.Time
	DecidedAt *time.Time
	Version   int64
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
func NewCase(id ids.UUID, verdict VerdictState, source, note string, now time.Time) *Case {
	return &Case{state: CaseState{
		ID:        id,
		TenantID:  verdict.TenantID,
		ProductID: verdict.ProductID,
		Revision:  verdict.Revision,
		Source:    source,
		Status:    "open",
		Reasons:   slices.Clone(verdict.Reasons),
		Note:      strings.TrimSpace(note),
		OpenedAt:  now,
		Version:   1,
	}}
}

// RestoreCase восстанавливает исходную версию для условного обновления.
func RestoreCase(s CaseState) *Case {
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
func (c *Case) Refresh(v VerdictState, now time.Time) bool {
	if c.state.Status != "open" {
		return false
	}
	if v.Status != Pending {
		c.state.Status = "superseded"
		c.state.DecidedAt = &now
	} else if c.state.Revision == v.Revision && reflect.DeepEqual(c.state.Reasons, v.Reasons) {
		return false
	}
	c.state.Revision = v.Revision
	c.state.Reasons = slices.Clone(v.Reasons)
	c.state.Version = c.origin.Version + 1
	return true
}

// Decide фиксирует решение для текущей ревизии.
func (c *Case) Decide(expected, revision int64, action, note string, now time.Time) (bool, error) {
	if expected < 1 || expected != c.state.Version || c.state.Status != "open" {
		return false, &CaseConflict{Current: c.State()}
	}
	if revision != c.state.Revision {
		return false, ErrCaseStale
	}
	if action != "allow" && action != "reject" {
		return false, ErrInvalid
	}
	note = strings.TrimSpace(note)
	if note == "" || utf8.RuneCountInString(note) > 2000 {
		return false, ErrInvalid
	}
	c.state.Status = Allowed
	if action == "reject" {
		c.state.Status = Rejected
	}
	c.state.Note = note
	c.state.DecidedAt = &now
	c.state.Version = c.origin.Version + 1
	return true, nil
}
