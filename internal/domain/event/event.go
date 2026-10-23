// Package event — жизненный цикл запаркованного сообщения и решение оператора.
package event

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Статусы жизненного цикла отравленного сообщения.
const (
	Parked   = "parked"
	Queued   = "queued"
	Resolved = "resolved"
)

// State — решение, версия и автор аудита; исходное сообщение хранится отдельно.
type State struct {
	ID         int64
	Status     string
	Version    int64
	Resolution string
	OperatorID string
	UpdatedAt  time.Time
}

// Event меняет состояние только переходами с ожидаемой исходной версией.
type Event struct {
	state  State
	origin State
}

// Restore восстанавливает агрегат.
func Restore(state State) *Event { return &Event{state: state, origin: state} }

// State — снимок для сохранения.
func (e *Event) State() State { return e.state }

// Origin — условия атомарного UPDATE.
func (e *Event) Origin() State { return e.origin }

// Decide разрешает ровно одно решение для состояния parked.
func (e *Event) Decide(retry bool, note, actor string, now time.Time) error {
	note = strings.TrimSpace(note)
	if note == "" || utf8.RuneCountInString(note) > 2000 {
		return ErrNote
	}
	if e.state.Status != Parked {
		return ErrConflict
	}
	e.state.Status = Resolved
	if retry {
		e.state.Status = Queued
	}
	e.state.Resolution = note
	e.state.OperatorID = actor
	e.state.UpdatedAt = now
	e.state.Version = e.origin.Version + 1
	return nil
}

// Finish фиксирует результат повтора. Заметка и автор решения сохраняются.
func (e *Event) Finish(success bool, now time.Time) error {
	if e.state.Status != Queued {
		return ErrConflict
	}
	e.state.Status = Parked
	if success {
		e.state.Status = Resolved
	}
	e.state.UpdatedAt = now
	e.state.Version = e.origin.Version + 1
	return nil
}

// MarkSaved фиксирует исходную версию после успешного условного обновления.
func (e *Event) MarkSaved() { e.origin = e.state }
