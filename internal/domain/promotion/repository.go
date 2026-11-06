package promotion

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Filter ограничивает выборку размещений; пустой лимит репозитория означает все строки.
type Filter struct {
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

// Repository привязан к транзакции; LockPlacement сериализует резервирование слотов.
type Repository interface {
	LockPlacement(context.Context, string) error
	Placements(context.Context) ([]PlacementState, error)
	Placement(context.Context, string) (*Placement, error)
	SavePlacement(context.Context, *Placement) error
	Get(context.Context, ids.UUID) (*Campaign, error)
	List(context.Context, Filter) ([]CampaignState, int, error)
	Save(context.Context, *Campaign) error
	Occupancy(context.Context, CampaignState, Scope, []string) ([]Week, error)
	AddDecision(context.Context, ids.UUID, Decision) error
	Decisions(context.Context, ids.UUID) ([]Decision, error)
	Count(context.Context, CampaignState, string, string, string) (bool, error)
	Stats(context.Context, ids.UUID, string, string) ([]Counter, error)
}
