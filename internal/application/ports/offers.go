package ports

import (
	"context"
	"encoding/json"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// OfferState — согласованный снимок всего, из чего собирается предложение:
// товар, сигналы, участник и запись реестра. Читается одним запросом.
type OfferState struct {
	Verdict     *moderation.Verdict
	Product     *catalog.Product
	Signals     *catalog.Signals
	Participant *participant.Participant
	Tenant      *participant.Tenant
}

// OfferStatus содержит только данные обратной связи; Err относится к одному повреждённому товару.
type OfferStatus struct {
	Key         ids.OfferID
	Revision    int64
	Deleted     bool
	Publication taxonomy.Publication
	Verdict     *moderation.Verdict
	Decision    *moderation.Decision
	CaseReasons []moderation.Reason
	Err         error
}

// OfferKey — позиция для постраничного чтения.
type OfferKey struct {
	TenantID  ids.TenantID
	ProductID ids.ProductID
}

// OfferStates — чтение проекции для индекса.
type OfferStates interface {
	// WalkStoreSummaryOffers читает единый снимок пачками, с соседними вариантами для подсчёта рейтинга.
	WalkStoreSummaryOffers(context.Context, ids.TenantID, func([]OfferState) error) error
	// StatusBatch читает статусы, последние решения и причины дел одним снимком.
	StatusBatch(context.Context, []ids.OfferID) ([]OfferStatus, error)
	// Batch читает проекции выбранных предложений одной пачкой.
	Batch(context.Context, []ids.OfferID) ([]OfferState, error)
	// Offer — снимок одного предложения; товара нет — catalog.ErrProductNotFound.
	Offer(ctx context.Context, tenantID ids.TenantID, productID ids.ProductID) (OfferState, error)
	// TenantOffers — предложения магазина после позиции after по возрастанию товара.
	TenantOffers(ctx context.Context, tenantID ids.TenantID, after ids.ProductID, limit int) ([]OfferState, error)
	// ParticipantOffers — предложения всех участников после позиции after.
	ParticipantOffers(ctx context.Context, after OfferKey, limit int) ([]OfferState, error)
	// ChangedSince — предложения участников, у которых товар, сигналы, участник
	// или запись реестра менялись не раньше since.
	ChangedSince(ctx context.Context, since time.Time, after OfferKey, limit int) ([]OfferState, error)
}

// OfferDocument — предложение в индексе поиска. Суммы — в копейках.
type OfferDocument struct {
	TileKey              string
	OnlyVariant          bool
	AttributeFacetValues map[string][]string
	AttributeSearchTerms map[string][]string
	Rating               *catalog.Rating
	Variants             *catalog.Variants
	ModelKey             string
	ModelKeySource       string
	VariantKey           string
	PriceProfiles        []catalog.PriceProfile
	CategoryID           string
	CategoryPath         []string
	Attributes           map[string]json.RawMessage
	ID                   string
	TenantID             string
	ProductID            string
	SellerSlug           string
	SellerName           string
	Delivery             participant.DeliverySettings
	Name                 string
	Description          string
	Image                string
	Assets               []string
	URL                  string
	SKU                  string
	Categories           []catalog.Category
	VariantsID           string
	Price                int64
	BasePrice            int64
	Available            int
	InStock              bool
	Visible              bool
	Listed               bool
	CheckoutAvailable    bool
	UpdatedAt            time.Time
	// Version растёт с каждым изменением любой части предложения; индекс не
	// принимает документ с версией меньше записанной.
	Version int64
}

// OfferSort — порядок выдачи.
type OfferSort string

const (
	// SortRelevance — по совпадению с запросом.
	SortRelevance OfferSort = "relevance"
	// SortPriceAsc — сначала дешёвые.
	SortPriceAsc OfferSort = "price_asc"
	// SortPriceDesc — сначала дорогие.
	SortPriceDesc OfferSort = "price_desc"
	// SortNewest — сначала обновлённые недавно.
	SortNewest OfferSort = "newest"
	// SortRating — сначала предложения с наибольшим средним баллом.
	SortRating OfferSort = "rating"
)

// MaxSearchAttributes — число фильтров в запросе контракта sc-search v5.
// Глобальный справочник таксономии имеет собственный, больший предел.
const MaxSearchAttributes = 256

// OfferQuery — поиск по выдаче каталога.
type OfferQuery struct {
	// Exact запрещает исправления опечаток, раскладки и транслита, сохраняя остальные правила поиска.
	Exact         bool
	TrackQuery    bool
	GroupID       string
	BuyerID       ids.UUID
	PriceContexts map[ids.TenantID]string
	InStockSet    bool
	Attributes    map[string]AttributeFilter
	FacetFields   []taxonomy.Attribute
	// nil сохраняет все фасеты; пустой список отключает их, attributes включает поля таксономии.
	Facets     *[]FacetCode
	Text       string
	TenantIDs  []string
	CategoryID string
	PriceMin   *int64
	PriceMax   *int64
	InStock    bool
	Sort       OfferSort
	Offset     int
	Limit      int
}

// OfferPage — страница выдачи.
type OfferPage struct {
	Correction *OfferCorrection
	Facets     []Facet
	Total      int
	Items      []OfferDocument
}

// OfferCorrection объясняет, по какому запросу найдены предложения после исправления или расширения.
type OfferCorrection struct {
	Query string
	Kind  string
}

// OfferGroup содержит ограниченную выборку вариантов продавца.
type OfferGroup struct {
	Items     []OfferDocument
	Truncated bool
}

// OfferIndex — индекс предложений.
type OfferIndex interface {
	Group(context.Context, string, string) (OfferGroup, error)
	// NeedsRebuild сообщает о незавершённой сборке или росте каталога.
	NeedsRebuild(ctx context.Context) (bool, error)
	// Exists — рабочий индекс заведён.
	Exists(ctx context.Context) (bool, error)
	// Put пишет документы в рабочий индекс; документ с версией ниже записанной пропускается.
	Put(ctx context.Context, docs []OfferDocument) error
	// Get — документ по идентификатору; нет — found=false.
	Get(ctx context.Context, id string) (doc OfferDocument, found bool, err error)
	// Search — выдача по запросу.
	Search(ctx context.Context, query OfferQuery) (OfferPage, error)
	// NewGeneration заводит пустое поколение индекса для перестроения.
	NewGeneration(ctx context.Context) (string, error)
	// PutGeneration пишет документы в поколение.
	PutGeneration(ctx context.Context, generation string, docs []OfferDocument) error
	// Activate делает поколение рабочим и удаляет прежние.
	Activate(ctx context.Context, generation string) error
}
