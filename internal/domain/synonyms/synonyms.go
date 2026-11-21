// Package synonyms задаёт жизненный цикл направленных поисковых правил.
package synonyms

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// SchemaVersion закрепляет структуру снимка, а квоты ограничивают платформенную работу.
const (
	SchemaVersion    = 1
	ValidatorVersion = 1
	MaxBatch         = 20
	DailyChecks      = 100
	DailyActivations = 20
	MaxActive        = 1000
	MaxCandidates    = 5000
	MaxSnapshot      = 512 << 10
	MaxBlocked       = 512
	MaxTermLength    = 64
	maxTermWords     = 8
	CandidateTTL     = 30 * 24 * time.Hour
	RecheckInterval  = 7 * 24 * time.Hour
	RetryInterval    = 24 * time.Hour

	Candidate   Phase = "candidate"
	Quarantined Phase = "quarantined"
	Active      Phase = "active"
	Disabled    Phase = "disabled"

	TaxonomyName  = "taxonomy_name"
	RegistryAlias = "registry_alias"
	Model         = "model"
	CategoryScope = "category"

	Pass   = "pass"
	Reject = "reject"
	Retry  = "retry"
)

// Ошибки словаря различают неверный запрос, конфликт ревизии и неизвестное направление.
var (
	ErrInvalid  = errs.New(errs.Invalid, "search_synonyms.invalid", "некорректные данные поискового словаря")
	ErrConflict = errs.New(errs.Conflict, "search_synonyms.conflict", "состояние поискового словаря изменилось")
	ErrNotFound = errs.New(errs.NotFound, "search_synonyms.not_found", "направление поискового словаря не найдено")
)

// Phase показывает, где направление находится в автомате допуска.
type Phase string

// Scope связывает направление с разделом общей таксономии.
type Scope struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Direction используется в запросе проверки и в опубликованном снимке.
type Direction struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Scope   Scope  `json:"scope"`
	Concept string `json:"concept,omitempty"`
}

// Rule передаёт направление и его текущую фазу.
type Rule struct {
	Direction
	Phase Phase `json:"phase"`
}

// Snapshot представляет целиком одну ревизию, пригодную для атомарной замены у читателя.
type Snapshot struct {
	SchemaVersion int      `json:"schemaVersion"`
	Revision      int64    `json:"revision"`
	Rules         []Rule   `json:"rules"`
	BlockedTerms  []string `json:"blockedTerms"`
}

// Source хранит происхождение и версии входа для повторной сверки перед публикацией.
type Source struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

// Nomination объединяет допустимое написание с обосновывающим его источником.
type Nomination struct {
	Direction
	Source Source `json:"source"`
}

// Evidence привязывает результат к проверенным каталогам и версии таксономии.
type Evidence struct {
	Fingerprint      string    `json:"fingerprint"`
	CheckedAt        time.Time `json:"checkedAt"`
	TaxonomyRevision string    `json:"taxonomyRevision"`
}

// Validation содержит признаки полноты, эффект и решение поиска.
type Validation struct {
	ID                  string   `json:"id"`
	Complete            bool     `json:"complete"`
	ScopesChecked       int      `json:"scopesChecked"`
	SourceLiteralScopes int      `json:"sourceLiteralScopes"`
	TargetLiteralScopes int      `json:"targetLiteralScopes"`
	RescuedScopes       int      `json:"rescuedScopes"`
	Decision            string   `json:"decision"`
	Evidence            Evidence `json:"evidence"`
}

// Check хранит исход валидатора и итог применения локальных условий допуска.
type Check struct {
	Validation
	BatchID          string    `json:"batchId"`
	ValidatorVersion int       `json:"validatorVersion"`
	RecordedAt       time.Time `json:"recordedAt"`
	Outcome          string    `json:"outcome"`
}

// Entry ведёт состояние направления и версию для условной записи.
type Entry struct {
	Rule
	Source      Source     `json:"source"`
	Version     int64      `json:"version"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	NextCheck   time.Time  `json:"nextCheck"`
	ActivatedAt *time.Time `json:"activatedAt,omitempty"`
	LastCheck   *Check     `json:"lastCheck,omitempty"`
}

// Transition сохраняет автора и причину изменения состояния.
type Transition struct {
	From   Phase
	To     Phase
	At     time.Time
	Actor  string
	Reason string
}

// NewNomination даёт одинаковый ID одному направлению при повторном сборе источников.
func NewNomination(from, to, category string, source Source) (Nomination, bool) {
	from, to = reference.Key(from), reference.Key(to)
	if !ValidTerm(from) || !ValidTerm(to) || from == to || category == "" || reference.Length(category) > 128 {
		return Nomination{}, false
	}
	sum := sha256.Sum256([]byte(category + "\x00" + from + "\x00" + to))
	return Nomination{
		Direction: Direction{
			ID:   "syn_" + hex.EncodeToString(sum[:16]),
			From: from,
			To:   to,
			Scope: Scope{
				Kind: CategoryScope,
				ID:   category,
			},
		},
		Source: source,
	}, true
}

// ValidTerm ограничивает объём одного выражения до проверки или публикации.
func ValidTerm(term string) bool {
	if length := reference.Length(term); length < 2 || length > MaxTermLength {
		return false
	}
	if strings.IndexFunc(term, unicode.IsLetter) < 0 || strings.IndexFunc(term, unicode.IsControl) >= 0 {
		return false
	}
	words := 0
	inWord := false
	for _, r := range term {
		word := wordRune(r)
		if word && !inWord {
			words++
		}
		inWord = word
	}
	return words <= maxTermWords
}

// NormalizeBlocked сводит варианты одного запрета к одному ключу снимка.
func NormalizeBlocked(terms []string) ([]string, error) {
	if len(terms) > MaxBlocked {
		return nil, ErrInvalid
	}
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		key := reference.Key(term)
		if !ValidTerm(key) {
			return nil, ErrInvalid
		}
		out = append(out, key)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// BoundaryTerms перечисляет возможные ключи бренда или стоп-листа в выражении.
func BoundaryTerms(term string) []string {
	runes := []rune(term)
	var starts, ends []int
	for i, r := range runes {
		if !wordRune(r) {
			continue
		}
		if i == 0 || !wordRune(runes[i-1]) {
			starts = append(starts, i)
		}
		if i == len(runes)-1 || !wordRune(runes[i+1]) {
			ends = append(ends, i+1)
		}
	}
	var keys []string
	for _, start := range starts {
		for _, end := range ends {
			if end > start {
				keys = append(keys, string(runes[start:end]))
			}
		}
	}
	return keys
}

func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r)
}

// Blocked сверяет каждую сторону с запретами по границам слов.
func (d Direction) Blocked(keys map[string]bool) bool {
	for _, term := range []string{d.From, d.To} {
		if keys[term] {
			return true
		}
		for _, part := range BoundaryTerms(term) {
			if keys[part] {
				return true
			}
		}
	}
	return false
}

// NewEntry открывает ограниченный срок первой каталожной проверки.
func NewEntry(n Nomination, now time.Time) Entry {
	return Entry{
		Rule: Rule{
			Direction: n.Direction,
			Phase:     Candidate,
		},
		Source:    n.Source,
		Version:   1,
		CreatedAt: now,
		ExpiresAt: now.Add(CandidateTTL),
		NextCheck: now,
	}
}

// Quarantine допускает исходную номинацию к очереди проверок.
func (e *Entry) Quarantine(now time.Time) (Transition, error) {
	if e.Phase != Candidate {
		return Transition{}, ErrConflict
	}
	return e.move(Quarantined, now, "worker", "source_collected"), nil
}

// Refresh начинает проверку заново после изменения исходных данных.
func (e *Entry) Refresh(source Source, now time.Time) {
	e.Source = source
	e.LastCheck = nil
	e.NextCheck = now
	e.ExpiresAt = now.Add(CandidateTTL)
	e.Version++
}

// Reserve отодвигает следующий вызов до обращения к поиску, включая сбой воркера.
func (e *Entry) Reserve(now time.Time) {
	e.NextCheck = now.Add(RetryInterval)
	e.Version++
}

// Record сохраняет проверку и назначает следующую сверку для активного направления.
func (e *Entry) Record(check Check, now time.Time) {
	switch {
	case check.Outcome == Pass && e.Phase == Active:
		e.NextCheck = now.Add(RecheckInterval)
	case check.Outcome == Reject && e.Phase == Quarantined && e.LastCheck != nil && e.LastCheck.Outcome == Reject:
		e.NextCheck = e.ExpiresAt
	}
	e.LastCheck = &check
	e.Version++
}

// Activate включает только карантинное направление с пройденной проверкой.
func (e *Entry) Activate(now time.Time) (Transition, error) {
	if e.Phase != Quarantined || e.LastCheck == nil || e.LastCheck.Outcome != Pass {
		return Transition{}, ErrConflict
	}
	e.ActivatedAt = &now
	e.NextCheck = now.Add(RecheckInterval)
	return e.move(Active, now, "worker", "catalog_pass"), nil
}

// Disable завершает жизненный цикл направления с причиной для журнала.
func (e *Entry) Disable(now time.Time, actor, reason string) (Transition, error) {
	if e.Phase != Quarantined && e.Phase != Active {
		return Transition{}, ErrConflict
	}
	return e.move(Disabled, now, actor, reason), nil
}

// RejectActive сохраняет отрицательную проверку и снимает направление одной записью.
func (e *Entry) RejectActive(check Check, now time.Time) (Transition, error) {
	if e.Phase != Active || check.Outcome != Reject {
		return Transition{}, ErrConflict
	}
	e.LastCheck = &check
	return e.Disable(now, "worker", "catalog_reject")
}

func (e *Entry) move(to Phase, now time.Time, actor, reason string) Transition {
	step := Transition{
		From:   e.Phase,
		To:     to,
		At:     now,
		Actor:  actor,
		Reason: reason,
	}
	e.Phase = to
	e.Version++
	return step
}

// Valid проверяет согласованность счётчиков, полноты и доказательства.
func (v Validation) Valid() bool {
	for _, count := range []int{v.SourceLiteralScopes, v.TargetLiteralScopes, v.RescuedScopes} {
		if count < 0 || count > v.ScopesChecked {
			return false
		}
	}
	if v.ScopesChecked < 0 || v.ScopesChecked > 5 {
		return false
	}
	if !v.Complete {
		return v.Decision == Retry
	}
	hash, err := hex.DecodeString(v.Evidence.Fingerprint)
	return (v.Decision == Pass || v.Decision == Reject) && v.ScopesChecked == 5 &&
		err == nil && len(hash) == sha256.Size && !v.Evidence.CheckedAt.IsZero() && v.Evidence.TaxonomyRevision != ""
}

// Outcome применяет пороги V1 после сверки версии таксономии и времени ответа.
func (v Validation) Outcome(taxonomyRevision string, now time.Time, registryAlias bool) string {
	if !v.Complete || v.Evidence.TaxonomyRevision != taxonomyRevision ||
		v.Evidence.CheckedAt.Before(now.Add(-RetryInterval)) || v.Evidence.CheckedAt.After(now.Add(5*time.Minute)) {
		return Retry
	}
	if v.Decision != Pass || v.TargetLiteralScopes == 0 || v.RescuedScopes < 3 ||
		(!registryAlias && v.SourceLiteralScopes == 0) {
		return Reject
	}
	return Pass
}
