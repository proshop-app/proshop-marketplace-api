package ports

import (
	"context"
	"encoding/json"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// OfferState — согласованный снимок всего, из чего собирается предложение:
// товар, сигналы, участник и запись реестра. Читается одним запросом.
type OfferState struct {
	StoreSummaryPending bool
	Verdict             *moderation.Verdict
	Product             *catalog.Product
	Signals             *catalog.Signals
	Participant         *participant.Participant
	Tenant              *participant.Tenant
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
	SalesTier            uint8
	TileKey              string
	AttributeFacetValues map[string][]string
	AttributeSearchTerms map[string][]string
	Rating               *catalog.Rating
	Variants             *catalog.Variants
	ModelKey             string
	ModelKeySource       string
	VariantKey           string
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
	FacetTaxonomyVersion int64
	// Exact запрещает исправления опечаток, раскладки и транслита, сохраняя остальные правила поиска.
	Exact       bool
	TrackQuery  bool
	Observe     bool
	BuyerKey    string
	GroupID     string
	InStockSet  bool
	Attributes  map[string]AttributeFilter
	FacetFields []taxonomy.Attribute
	// nil сохраняет все фасеты; пустой список отключает их, attributes включает поля таксономии.
	FacetKeys      []string
	PriceHistogram bool
	Facets         *[]FacetCode
	Text           string
	TenantIDs      []string
	CategoryID     string
	PriceMin       *int64
	PriceMax       *int64
	InStock        bool
	Sort           OfferSort
	Offset         int
	Limit          int
}

// OfferPage — страница выдачи.
type OfferPage struct {
	FacetCategoryID    string
	AvailableFacetKeys []string
	Correction         *OfferCorrection
	Receipt            string
	Facets             []Facet
	Total              int
	TotalRelation      string
	Limited            bool
	Items              []OfferDocument
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

// OfferCapacityError сообщает об отложенных предложениях уже разобранного
// префикса Put. Хвост от UnconfirmedFrom отдельным результатом не подтверждён.
type OfferCapacityError struct {
	Deferred         []ids.OfferID
	UnconfirmedFrom  int
	UnconfirmedError error
}

// ErrOfferCapacity оставляет задачу предложения для повтора.
var ErrOfferCapacity = errs.New(errs.Unavailable, "search.generation_capacity", "часть индекса предложений заполнена")

// OfferIndex — индекс предложений.
type OfferIndex interface {
	Group(context.Context, string, string) (OfferGroup, error)
	// NeedsRebuild сообщает о незавершённой сборке или росте каталога.
	NeedsRebuild(ctx context.Context) (bool, error)
	// Exists — рабочий индекс заведён.
	Exists(ctx context.Context) (bool, error)
	// Put пишет документы в рабочий индекс, пропуская устаревшие версии.
	// Частичное подтверждение возвращается как OfferCapacityError.
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

func (e *OfferCapacityError) Error() string {
	return ErrOfferCapacity.Error()
}

func (e *OfferCapacityError) Unwrap() error {
	return ErrOfferCapacity
}
