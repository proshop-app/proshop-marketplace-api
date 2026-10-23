package catalog

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// StockSignal — свободный остаток товара из события склада.
type StockSignal struct {
	Available int
	At        time.Time
}

// PriceSignal — прайс товара из события смены цены или склада.
type PriceSignal struct {
	Price money.Money
	At    time.Time
}

// SignalsState — плоский снимок сигналов по товару.
type SignalsState struct {
	TenantID  ids.TenantID
	ProductID ids.ProductID
	HasStock  bool
	Available int
	StockAt   time.Time
	HasPrice  bool
	Price     money.Money
	PriceAt   time.Time
	Version   int64
	UpdatedAt time.Time
}

// Signals — остаток и цена товара, пришедшие отдельно от снимка карточки.
// У событий склада и цены нет ревизии, поэтому они упорядочиваются временем
// публикации.
type Signals struct {
	state         SignalsState
	originVersion int64
}

// NewSignals — пустые сигналы товара.
func NewSignals(tenantID ids.TenantID, productID ids.ProductID) *Signals {
	return &Signals{state: SignalsState{TenantID: tenantID, ProductID: productID}}
}

// RestoreSignals поднимает сигналы из хранилища.
func RestoreSignals(state SignalsState) *Signals {
	return &Signals{state: state, originVersion: state.Version}
}

// ApplyStock принимает остаток, если он не старше уже известного.
func (s *Signals) ApplyStock(signal StockSignal, now time.Time) (bool, error) {
	if signal.At.IsZero() {
		return false, ErrInvalidSignal.WithMessage("у сигнала остатка нет времени")
	}

	available := signal.Available
	if available < 0 {
		available = 0
	}

	if s.state.HasStock && (signal.At.Before(s.state.StockAt) ||
		(signal.At.Equal(s.state.StockAt) && available == s.state.Available)) {
		return false, nil
	}

	s.state.HasStock = true
	s.state.Available = available
	s.state.StockAt = signal.At
	s.touch(now)

	return true, nil
}

// ApplyPrice принимает прайс, если он не старше уже известного.
func (s *Signals) ApplyPrice(signal PriceSignal, now time.Time) (bool, error) {
	if signal.At.IsZero() {
		return false, ErrInvalidSignal.WithMessage("у сигнала цены нет времени")
	}
	if signal.Price.IsNegative() {
		return false, ErrInvalidSignal.WithMessage("отрицательная цена")
	}
	if s.state.HasPrice && (signal.At.Before(s.state.PriceAt) ||
		(signal.At.Equal(s.state.PriceAt) && signal.Price.Cmp(s.state.Price) == 0)) {
		return false, nil
	}

	s.state.HasPrice = true
	s.state.Price = signal.Price
	s.state.PriceAt = signal.At
	s.touch(now)

	return true, nil
}

func (s *Signals) touch(now time.Time) {
	s.state.Version = s.originVersion + 1
	s.state.UpdatedAt = now
}

// State — снимок для хранения.
func (s *Signals) State() SignalsState { return s.state }

// OriginVersion — версия на момент чтения; у новой записи — ноль.
func (s *Signals) OriginVersion() int64 { return s.originVersion }

// MarkSaved фиксирует сохранённую версию.
func (s *Signals) MarkSaved() { s.originVersion = s.state.Version }

// Version — версия сигналов.
func (s *Signals) Version() int64 { return s.state.Version }
