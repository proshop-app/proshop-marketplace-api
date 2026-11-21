package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/synonyms"
)

// SynonymValidationRequest связывает направления одним идентификатором проверки.
type SynonymValidationRequest struct {
	BatchID string                  `json:"batchId"`
	Rules   []SynonymValidationRule `json:"rules"`
}

// SynonymValidationRule добавляет указание, когда для подтверждённого варианта допустим ноль по from.
type SynonymValidationRule struct {
	synonyms.Direction
	Source string `json:"source,omitempty"`
}

// SynonymValidationResponse содержит исход для каждого направления исходной пачки.
type SynonymValidationResponse struct {
	BatchID          string                `json:"batchId"`
	ValidatorVersion int                   `json:"validatorVersion"`
	Results          []synonyms.Validation `json:"results"`
}

// SynonymValidator выполняет пробы на каталогах без участия площадки в выборе продавцов.
type SynonymValidator interface {
	Validate(context.Context, SynonymValidationRequest) (SynonymValidationResponse, error)
}

// SynonymHead хранит общую ревизию, квоты и дату публикации под одной блокировкой.
type SynonymHead struct {
	Revision      int64
	LastPublished string
	BudgetDay     string
	Checked       int
	ModelCalls    int
	ModelInput    string
	BlockedTerms  []string
}

// SynonymInput связывает доверенное написание с известным именем раздела.
type SynonymInput struct {
	Category     string
	CategoryName string
	From         string
	To           string
	Source       synonyms.Source
}

// SynonymRepository работает внутри транзакции с одной общей головой словаря.
type SynonymRepository interface {
	Current(context.Context, bool) (SynonymHead, error)
	Snapshot(context.Context, int64) (int64, []byte, error)
	SaveHead(context.Context, SynonymHead, []byte) error
	Inputs(context.Context) ([]SynonymInput, error)
	Brands(context.Context, []string) ([]string, error)
	Entries(context.Context, time.Time, string, int) ([]synonyms.Entry, error)
	Find(context.Context, string) (synonyms.Entry, error)
	Known(context.Context, []string) ([]synonyms.Entry, error)
	Active(context.Context) ([]synonyms.Entry, error)
	Due(context.Context, time.Time, int, []string) ([]synonyms.Entry, error)
	Ready(context.Context, time.Time, int, []string) ([]synonyms.Entry, error)
	LiveCount(context.Context, time.Time) (int, error)
	Insert(context.Context, synonyms.Entry) error
	Save(context.Context, synonyms.Entry, synonyms.Entry) error
	RecordTransition(context.Context, string, synonyms.Transition) error
	RecordCheck(context.Context, synonyms.Check) error
}

// SynonymModelConcept содержит только опубликованные сведения о виде товара.
type SynonymModelConcept struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Definition string `json:"definition,omitempty"`
}

// SynonymModelProposal связывает одно направление и заявленное отношение с видом товара.
type SynonymModelProposal struct {
	Category     string `json:"categoryId"`
	CategoryName string
	From         string `json:"from"`
	To           string `json:"to"`
	Relation     string `json:"relation"`
}

// SynonymModel предлагает направления для отдельного карантина.
type SynonymModel interface {
	Identity() string
	Propose(context.Context, string, []SynonymModelConcept) ([]SynonymModelProposal, error)
}
