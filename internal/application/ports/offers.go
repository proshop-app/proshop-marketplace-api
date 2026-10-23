package ports

import (
	"context"
	"encoding/json"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// OfferState — согласованный снимок всего, из чего собирается предложение:
// товар, сигналы, участник и запись реестра. Читается одним запросом.
type OfferState struct {
	Product     *catalog.Product
	Signals     *catalog.Signals
	Participant *participant.Participant
	Tenant      *participant.Tenant
}

// OfferKey — позиция для постраничного чтения.
type OfferKey struct {
	TenantID  ids.TenantID
	ProductID ids.ProductID
}

// OfferStates — чтение проекции для индекса.
type OfferStates interface {
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
	CategoryID        string
	CategoryPath      []string
	Attributes        map[string]json.RawMessage
	ID                string
	TenantID          string
	ProductID         string
	SellerSlug        string
	SellerName        string
	Name              string
	Description       string
	Image             string
	URL               string
	SKU               string
	Categories        []catalog.Category
	Tags              []string
	VariantsID        string
	Price             int64
	BasePrice         int64
	Available         int
	InStock           bool
	Visible           bool
	Listed            bool
	CheckoutAvailable bool
	UpdatedAt         time.Time
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
)

// OfferQuery — поиск по выдаче каталога.
type OfferQuery struct {
	InStockSet  bool
	Attributes  map[string]AttributeFilter
	FacetFields []taxonomy.Attribute
	Text        string
	TenantID    string
	CategoryID  string
	Tag         string
	PriceMin    *int64
	PriceMax    *int64
	InStock     bool
	Sort        OfferSort
	Offset      int
	Limit       int
}

// OfferPage — страница выдачи.
type OfferPage struct {
	Facets []Facet
	Total  int
	Items  []OfferDocument
}

// OfferIndex — индекс предложений.
type OfferIndex interface {
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
