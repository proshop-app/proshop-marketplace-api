package promotion

import (
	"maps"
	"slices"

	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Scope определяет общий лимит либо отдельный лимит каждой категории.
type Scope string

// Значения контракта продвижения не зависят от конкретного магазина.
const (
	Category Scope = "category"
	Global   Scope = "global"
)

// PlacementState снимок прайса; цены задаются оператором.
type PlacementState struct {
	Code           string
	Surface        string
	Title          string
	Positions      []int
	SlotLimit      int
	DefaultPrice   money.Money
	PositionPrices map[int]money.Money
	Active         bool
	Scope          Scope
	Version        int64
}

// Placement читает исходный снимок прайса.
type Placement struct {
	state  PlacementState
	origin int64
}

// RestorePlacement копирует изменяемые коллекции и сохраняет исходную версию.
func RestorePlacement(s PlacementState) *Placement {
	s.Positions = slices.Clone(s.Positions)
	s.PositionPrices = maps.Clone(s.PositionPrices)
	return &Placement{state: s, origin: s.Version}
}

// State возвращает отделённый от агрегата снимок.
func (p *Placement) State() PlacementState { return RestorePlacement(p.state).state }

// OriginVersion возвращает версию для обнаружения параллельной правки.
func (p *Placement) OriginVersion() int64 { return p.origin }

// MarkSaved начинает новый цикл изменений после успешной записи.
func (p *Placement) MarkSaved() { p.origin = p.state.Version }

// Price использует цену позиции, затем общий прайс места.
func (p *Placement) Price(position int) money.Money {
	if price, ok := p.state.PositionPrices[position]; ok {
		return price
	}
	return p.state.DefaultPrice
}

// Configure проверяет весь прайс до атомарного изменения места.
func (p *Placement) Configure(limit int, price money.Money, prices map[int]money.Money, active bool) (bool, error) {
	if limit < 1 || price.IsNegative() {
		return false, ErrInvalid
	}
	for position, value := range prices {
		if !slices.Contains(p.state.Positions, position) || value.IsNegative() {
			return false, ErrInvalid
		}
	}
	for _, position := range p.state.Positions {
		value := price
		if override, ok := prices[position]; ok {
			value = override
		}
		if active && value.Kopecks() <= 0 {
			return false, ErrInvalid
		}
	}
	if p.state.SlotLimit == limit && p.state.DefaultPrice == price &&
		maps.Equal(p.state.PositionPrices, prices) && p.state.Active == active {
		return false, nil
	}
	p.state.SlotLimit = limit
	p.state.DefaultPrice = price
	p.state.PositionPrices = maps.Clone(prices)
	p.state.Active = active
	p.state.Version = p.origin + 1
	return true, nil
}

// Quote рассчитывает цену периода с контролем переполнения.
func (p *Placement) Quote(position, weeks int) (money.Money, error) {
	if !p.state.Active || !slices.Contains(p.state.Positions, position) || weeks < 1 {
		return money.Money{}, ErrInvalid
	}
	return p.Price(position).MulRatio(int64(weeks), 1)
}
