package moderation

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

var reasonCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Actor сохраняет идентичность оператора в неизменяемом журнале.
type Actor struct {
	ID   string
	Name string
}

// Decision хранит основание решения для последующего аудита.
type Decision struct {
	ID       ids.UUID
	CaseID   ids.UUID
	Operator Actor
	Action   string
	Reason   string
	Note     string
	Revision int64
	At       time.Time
}

// StopPhrase сохраняет исходный текст и единый ключ сопоставления.
type StopPhrase struct {
	Text      string
	Key       string
	Operator  Actor
	CreatedAt time.Time
}

// ReasonCode отделяет стабильный код причины от отображаемого текста.
type ReasonCode struct {
	Code string
	Text string
}

// NewStopPhrase нормализует пробелы до проверки ограничений длины.
func NewStopPhrase(text string, operator Actor, now time.Time) (StopPhrase, error) {
	text = strings.Join(strings.Fields(text), " ")
	size := utf8.RuneCountInString(text)
	if size < 2 || size > 120 {
		return StopPhrase{}, ErrInvalid
	}
	return StopPhrase{Text: text, Key: Key(text), Operator: operator, CreatedAt: now}, nil
}

// Validate не допускает некорректные данные в словарь.
func (r ReasonCode) Validate() error {
	if !reasonCode.MatchString(r.Code) || strings.TrimSpace(r.Text) == "" || utf8.RuneCountInString(r.Text) > 200 {
		return ErrInvalid
	}
	return nil
}
