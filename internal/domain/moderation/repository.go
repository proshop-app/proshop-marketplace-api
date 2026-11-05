package moderation

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Filter ограничивает выборку очереди для постраничного чтения.
type Filter struct {
	Status   Status
	TenantID ids.TenantID
	Source   string
	Limit    int
	Offset   int
}

// VerdictRepository сохраняет вердикты с контролем исходной версии.
type VerdictRepository interface {
	Get(context.Context, ids.TenantID, ids.ProductID) (*Verdict, error)
	Save(context.Context, *Verdict) error
	List(context.Context, Filter) ([]VerdictState, int, error)
}

// CaseRepository гарантирует единственность открытого дела и условное обновление.
type CaseRepository interface {
	OpenStats(context.Context) ([]OpenStat, error)
	Get(context.Context, ids.UUID) (*Case, error)
	Open(context.Context, ids.TenantID, ids.ProductID) (*Case, error)
	Save(context.Context, *Case) error
	List(context.Context, Filter) ([]CaseState, int, error)
}

// DecisionRepository разрешает только добавление и чтение журнала.
type DecisionRepository interface {
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
	RemoveReason(context.Context, string) error
}

// CachedDecision хранит исходный ответ независимо от дальнейшего состояния дела.
type CachedDecision struct {
	Fingerprint string
	Result      CaseState
	ExpiresAt   time.Time
}

// OpenStat агрегирует очередь одним запросом независимо от числа страниц.
type OpenStat struct {
	TenantID ids.TenantID
	Count    int
	Oldest   time.Time
}
