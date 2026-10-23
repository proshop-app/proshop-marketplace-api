// Package catalog — проекция каталогов магазинов и предложения маркетплейса.
// Проекция не источник истины: перед оплатой цена и наличие перепроверяются
// у магазина.
package catalog

import (
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Category — категория товара в каталоге магазина.
type Category struct {
	ID   string
	Name string
	URL  string
}

// Card — то, что покупатель видит о товаре.
type Card struct {
	Marketplace *taxonomy.Card
	Publication taxonomy.Publication
	Name        string
	Description string
	Image       string
	URL         string
	SKU         string
	Categories  []Category
	Tags        []string
	VariantsID  string
	// Visible — товар включён в каталоге магазина.
	Visible bool
	// OnlyVariant — товар показывается только внутри группы вариантов, не в выдаче.
	OnlyVariant bool
}

// Snapshot — снимок товара из события проекции: полная карточка или удаление
// с монотонной ревизией источника.
type Snapshot struct {
	TenantID   ids.TenantID
	ProductID  ids.ProductID
	Revision   int64
	Deleted    bool
	Card       Card
	Price      money.Money
	PromoPrice *money.Money
	Available  int
	// At — время публикации события; по нему снимок сравнивается с сигналами.
	At time.Time
}

// ProductState — плоский снимок записи проекции.
type ProductState struct {
	TenantID    ids.TenantID
	ProductID   ids.ProductID
	Revision    int64
	Deleted     bool
	Card        Card
	Price       money.Money
	PromoPrice  *money.Money
	Available   int
	ProjectedAt time.Time
	Version     int64
	UpdatedAt   time.Time
}

// Product — товар магазина в проекции маркетплейса.
type Product struct {
	state         ProductState
	originVersion int64
}

// NewProduct заводит запись из первого снимка.
func NewProduct(snapshot Snapshot, now time.Time) (*Product, error) {
	if err := snapshot.validate(); err != nil {
		return nil, err
	}

	product := &Product{}
	product.assign(snapshot, now)

	return product, nil
}

// RestoreProduct поднимает запись из хранилища.
func RestoreProduct(state ProductState) *Product {
	return &Product{state: state, originVersion: state.Version}
}

// Apply применяет снимок с ревизией больше текущей; повтор и старые ревизии
// не меняют запись — так переигрывание топика безопасно.
func (p *Product) Apply(snapshot Snapshot, now time.Time) (bool, error) {
	if err := snapshot.validate(); err != nil {
		return false, err
	}
	if snapshot.TenantID != p.state.TenantID || snapshot.ProductID != p.state.ProductID {
		return false, ErrInvalidSnapshot.WithMessage("снимок другого товара")
	}
	if snapshot.Revision <= p.state.Revision {
		return false, nil
	}

	p.assign(snapshot, now)

	return true, nil
}

func (p *Product) assign(snapshot Snapshot, now time.Time) {
	available := snapshot.Available
	if available < 0 {
		available = 0
	}

	p.state.TenantID = snapshot.TenantID
	p.state.ProductID = snapshot.ProductID
	p.state.Revision = snapshot.Revision
	p.state.Deleted = snapshot.Deleted
	p.state.ProjectedAt = snapshot.At
	p.state.Version = p.originVersion + 1
	p.state.UpdatedAt = now

	if snapshot.Deleted {
		p.state.Card = Card{}
		p.state.Price = money.Money{}
		p.state.PromoPrice = nil
		p.state.Available = 0

		return
	}

	p.state.Card = snapshot.Card
	p.state.Card.Name = strings.TrimSpace(snapshot.Card.Name)
	p.state.Price = snapshot.Price
	p.state.PromoPrice = snapshot.PromoPrice
	p.state.Available = available
}

// State — снимок для хранения.
func (p *Product) State() ProductState { return p.state }

// OriginVersion — версия на момент чтения; у новой записи — ноль.
func (p *Product) OriginVersion() int64 { return p.originVersion }

// MarkSaved фиксирует сохранённую версию.
func (p *Product) MarkSaved() { p.originVersion = p.state.Version }

// Version — версия записи: растёт на каждое применённое изменение.
func (p *Product) Version() int64 { return p.state.Version }

func (s Snapshot) validate() error {
	switch {
	case s.Revision < 1:
		return ErrInvalidSnapshot.WithMessage("ревизия снимка должна быть больше нуля")
	case s.At.IsZero():
		return ErrInvalidSnapshot.WithMessage("у снимка нет времени")
	case s.Deleted:
		return nil
	case strings.TrimSpace(s.Card.Name) == "":
		return ErrInvalidSnapshot.WithMessage("у товара нет названия")
	case s.Price.IsNegative():
		return ErrInvalidSnapshot.WithMessage("отрицательная цена товара")
	case s.PromoPrice != nil && s.PromoPrice.IsNegative():
		return ErrInvalidSnapshot.WithMessage("отрицательная промо-цена товара")
	default:
		return nil
	}
}
