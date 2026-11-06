package promotion

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Filter ограничивает выборку размещений; пустой лимит репозитория означает все строки.
type Filter struct {
	ProductID     ids.ProductID
	Status        Status
	Kind          Kind
	TenantID      ids.TenantID
	PlacementCode string
	From          string
	To            string
	Limit         int
	Offset        int
}

// Counter представляет суточные счётчики позиции.
type Counter struct {
	Day         string `json:"day"`
	Position    int    `json:"position"`
	Impressions int64  `json:"impressions"`
	Clicks      int64  `json:"clicks"`
}

// Summary — итоги размещения и имя магазина для чтения.
type Summary struct {
	TenantName  string
	Impressions int64
	Clicks      int64
}

// ReportGroup объединяет одинаковые условия для сводного отчёта.
type ReportGroup struct {
	CampaignState
	Count int
}

// Repository привязан к транзакции; LockPlacement сериализует резервирование слотов.
type Repository interface {
	LockPlacement(context.Context, string) error
	Placements(context.Context) ([]PlacementState, error)
	Placement(context.Context, string) (*Placement, error)
	SavePlacement(context.Context, *Placement) error
	Get(context.Context, ids.UUID) (*Campaign, error)
	List(context.Context, Filter) ([]CampaignState, int, error)
	Save(context.Context, *Campaign) error
	Delete(context.Context, *Campaign) error
	Counts(context.Context, Filter) (map[Status]int, error)
	Summaries(context.Context, []ids.UUID) (map[ids.UUID]Summary, error)
	Traffic(context.Context, string, string) ([]Counter, error)
	ReportGroups(context.Context, string, string) ([]ReportGroup, error)
	Occupancy(context.Context, CampaignState, Scope, []string, ids.UUID) ([]Week, error)
	AddDecision(context.Context, ids.UUID, Decision) error
	Decisions(context.Context, ids.UUID) ([]Decision, error)
	Count(context.Context, CampaignState, string, string, string) (bool, error)
	Stats(context.Context, ids.UUID, string, string) ([]Counter, error)
}
