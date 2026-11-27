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

// Selection — внутренний отбор без COUNT и OFFSET; пустой список Offers исключает все товары.
type Selection struct {
	ByID       bool
	EndBefore  string
	Filter     Filter
	CategoryID string
	Offers     []ids.OfferID
	Occupying  bool
	After      ids.UUID
	Limit      int
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

// Beacon — одно доставленное событие продвижения.
type Beacon struct {
	EventID     ids.UUID `json:"eventId"`
	PromotionID ids.UUID `json:"promotionId"`
	Kind        string   `json:"kind"`
}

// CountedBeacon содержит метки для события, впервые принятого счётчиком.
type CountedBeacon struct {
	TenantID      ids.TenantID
	PlacementCode string
	Kind          string
}

// Repository привязан к транзакции; LockPlacement сериализует резервирование слотов.
type Repository interface {
	LockPlacement(context.Context, string) error
	Placements(context.Context) ([]PlacementState, error)
	Placement(context.Context, string) (*Placement, error)
	SavePlacement(context.Context, *Placement) error
	Get(context.Context, ids.UUID) (*Campaign, error)
	Select(context.Context, Selection) ([]CampaignState, error)
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
	CountEvents(ctx context.Context, day string, events []Beacon) ([]CountedBeacon, error)
	Stats(context.Context, ids.UUID, string, string) ([]Counter, error)
}
