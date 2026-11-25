package moderation

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Filter ограничивает выборку очереди для постраничного чтения.
type Filter struct {
	Order     string
	Reason    string
	DecidedBy string
	Status    Status
	TenantID  ids.TenantID
	Source    string
	Limit     int
	Offset    int
}

// VerdictRepository сохраняет вердикты с контролем исходной версии.
type VerdictRepository interface {
	History(context.Context, ids.TenantID, ids.ProductID) ([]VerdictState, error)
	Get(context.Context, ids.TenantID, ids.ProductID) (*Verdict, error)
	// GetMany читает вердикты списка одним запросом; отсутствующих в ответе нет.
	GetMany(context.Context, []ids.OfferID) (map[ids.OfferID]*Verdict, error)
	Save(context.Context, *Verdict) error
	// SaveMany — Save для пачки одним запросом; конфликт любого вердикта — ErrConflict.
	SaveMany(context.Context, []*Verdict) error
	List(context.Context, Filter) ([]VerdictState, int, error)
}

// CaseRepository гарантирует единственность открытого дела и условное обновление.
type CaseRepository interface {
	Reasons(context.Context, []ids.UUID) (map[ids.UUID][]Reason, error)
	OpenStats(context.Context) (OpenStat, error)
	Get(context.Context, ids.UUID) (*Case, error)
	Open(context.Context, ids.TenantID, ids.ProductID) (*Case, error)
	// OpenMany читает открытые дела списка одним запросом; товаров без открытого дела в ответе нет.
	OpenMany(context.Context, []ids.OfferID) (map[ids.OfferID]*Case, error)
	Save(context.Context, *Case) error
	List(context.Context, Filter) ([]CaseState, int, error)
}

// DecisionRepository разрешает только добавление и чтение журнала.
type DecisionRepository interface {
	Latest(context.Context, ids.TenantID, []ids.ProductID) (map[ids.ProductID]Decision, error)
	Add(context.Context, Decision) error
	Cached(context.Context, string, time.Time) (CachedDecision, bool, error)
	Cache(context.Context, string, CachedDecision) error
	List(context.Context, ids.TenantID, ids.ProductID) ([]Decision, error)
}

// RulesRepository связывает изменение стоп-фраз с ревизией правил.
type RulesRepository interface {
	Current(context.Context) (Rules, error)
	AddPhrase(context.Context, StopPhrase) error
	RemovePhrase(context.Context, string) (bool, error)
	ListReasons(context.Context) ([]ReasonCode, error)
	PutReason(context.Context, ReasonCode) error
	AddReason(context.Context, ReasonCode) error
}

// CachedDecision хранит исходный ответ независимо от дальнейшего состояния дела.
type CachedDecision struct {
	Fingerprint string
	Result      CaseState
	ExpiresAt   time.Time
}

// OpenStat агрегирует очередь всей площадки одним запросом: магазин — не метка метрики.
type OpenStat struct {
	Count  int
	Oldest time.Time
}

// ResolveReason фиксирует текст из общего словаря до записи решения.
func ResolveReason(ctx context.Context, repo RulesRepository, code string) (string, error) {
	if code == "" {
		return "", nil
	}
	reasons, err := repo.ListReasons(ctx)
	if err != nil {
		return "", err
	}
	for _, reason := range reasons {
		if reason.Code == code {
			return reason.Text, nil
		}
	}
	return "", ErrReasonUnknown
}

// DecisionReason возвращает отсутствие причины явно, сохраняя исторический текст.
func DecisionReason(code, message string) *ReasonCode {
	if code == "" {
		return nil
	}
	return &ReasonCode{Code: code, Text: message}
}
