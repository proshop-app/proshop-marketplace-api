// Package moderation определяет допуск предложений политикой площадки.
package moderation

import (
	"strings"
	"unicode"
)

// Значения статусов сохраняются в БД и проверяются ограничениями схемы.
const (
	Open       Status = "open"
	Superseded Status = "superseded"
	Allow      Action = "allow"
	Reject     Action = "reject"

	Allowed  Status = "allowed"
	Pending  Status = "pending"
	Rejected Status = "rejected"
	Rule            = "rule"
	Operator        = "operator"
)

// Status объединяет состояния допуска и жизненного цикла дела.
type Status string

// Action ограничивает решения оператора над делом.
type Action string

// Reason сохраняет стабильный ключ и объяснение ограничения видимости.
type Reason struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

// Input отделяет правила допуска от структуры каталога.
type Input struct {
	Policy       string
	Name         string
	Description  string
	Image        string
	Probation    bool
	ManualReject bool
}

// Rules объединяет настройки оценки и ревизию операторского словаря.
type Rules struct {
	Revision     int64
	RequireImage bool
	Phrases      []StopPhrase
}

// Key даёт одинаковое сопоставление названий, описаний и стоп-фраз.
func Key(text string) string {
	return strings.ReplaceAll(strings.ToLower(strings.Join(strings.FieldsFunc(text, unicode.IsSpace), " ")), "ё", "е")
}

// Evaluate останавливает оценку на первом отказе и накапливает причины ручной проверки.
func (r Rules) Evaluate(in Input) (Status, []Reason) {
	reasons := []Reason{}
	add := func(key, message string) {
		reasons = append(reasons, Reason{Key: "moderation." + key, Message: message})
	}
	if in.Policy == "forbidden" {
		add("category_forbidden", "категория запрещена")
		return Rejected, reasons
	}
	if in.Policy == "manual" {
		add("category_manual", "категория требует проверки")
	}
	for _, phrase := range r.Phrases {
		if strings.Contains(Key(in.Name), phrase.Key) || strings.Contains(Key(in.Description), phrase.Key) {
			add("stop_phrase", phrase.Text)
		}
	}
	if r.RequireImage && strings.TrimSpace(in.Image) == "" {
		add("image_missing", "нет изображения")
		return Rejected, reasons
	}
	if in.Probation {
		add("probation", "участник на испытании")
	}
	if in.ManualReject {
		add("rejected_revision", "Предыдущая ревизия отклонена оператором")
	}
	if len(reasons) > 0 {
		return Pending, reasons
	}
	return Allowed, reasons
}

// NotEvaluated централизует причину скрытия старых проекций.
func NotEvaluated() Reason {
	return Reason{Key: "moderation.not_evaluated", Message: "предложение ещё не оценено"}
}
