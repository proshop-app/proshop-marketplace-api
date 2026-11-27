// Package cart — межмагазинная корзина покупателя.
package cart

import (
	"slices"
	"sort"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	// MaxQuantity — наибольшее количество в строке.
	MaxQuantity = 999
	// MaxLines — наибольшее число строк.
	MaxLines = 100
)

// Issue — что не так со строкой после перепроверки у магазина.
type Issue string

const (
	// IssueNone — строка в порядке.
	IssueNone Issue = ""
	// IssuePriceChanged — магазин поменял цену; в строке уже новая.
	IssuePriceChanged Issue = "price_changed"
	// IssueNotEnoughStock — у магазина меньше, чем в строке.
	IssueNotEnoughStock Issue = "not_enough_stock"
	// IssueUnavailable — магазин больше не продаёт предложение.
	IssueUnavailable Issue = "unavailable"
)

// Offer — предложение, как его видит корзина в момент действия.
type Offer struct {
	ID        ids.OfferID
	Name      string
	Price     money.Money
	Available int
	// Visible — предложение открыто покупателю.
	Visible bool
}

// Line — строка корзины: предложение со снимком цены на момент добавления
// или последней перепроверки.
type Line struct {
	Position  int64 `json:"-"`
	Discounts []Discount
	Offer     ids.OfferID
	Name      string
	Quantity  int
	Price     money.Money
	Issue     Issue
	AddedAt   time.Time
	CheckedAt time.Time
}

// Check — ответ магазина по строке.
type Check struct {
	Offer     ids.OfferID
	Exists    bool
	Price     money.Money
	Available int
}

// Group — строки одного магазина.
type Group struct {
	PromoCode *PromoCode
	TenantID  ids.TenantID
	Lines     []Line
}

// State — плоский снимок корзины.
type State struct {
	Promos    map[ids.TenantID]PromoCode
	ID        ids.UUID
	BuyerID   ids.UUID
	Lines     []Line
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt time.Time
}

// Cart — корзина покупателя: одна на покупателя, живёт ttl с последнего изменения.
type Cart struct {
	originLines   []Line
	state         State
	originVersion int64
}

// New — пустая корзина.
func New(id, buyerID ids.UUID, now time.Time, ttl time.Duration) *Cart {
	return &Cart{
		state: State{ID: id, BuyerID: buyerID, Version: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(ttl)},
	}
}

// Restore поднимает корзину из хранилища.
func Restore(state State) *Cart {
	c := &Cart{state: state, originVersion: state.Version}
	c.state = c.State()
	c.originLines = cloneLines(c.state.Lines)
	return c
}

// State — снимок для хранения.
func (c *Cart) State() State {
	state := c.state
	state.Promos = ClonePromos(c.state.Promos)
	state.Lines = cloneLines(c.state.Lines)
	return state
}

func cloneLines(lines []Line) []Line {
	copyOfLines := slices.Clone(lines)
	for i := range copyOfLines {
		copyOfLines[i].Discounts = slices.Clone(lines[i].Discounts)
	}
	return copyOfLines
}

// OriginVersion — версия на момент чтения; у новой корзины — ноль.
func (c *Cart) OriginVersion() int64 { return c.originVersion }

// MarkSaved фиксирует сохранённую версию.
func (c *Cart) MarkSaved() {
	c.originVersion = c.state.Version
	c.originLines = cloneLines(c.state.Lines)
}

// LineChanges возвращает только строки, изменившиеся со времени загрузки или сохранения.
func (c *Cart) LineChanges() (saved []Line, removed []ids.OfferID) {
	origin := make(map[ids.OfferID]Line, len(c.originLines))
	for _, line := range c.originLines {
		origin[line.Offer] = line
	}
	for _, line := range c.state.Lines {
		previous, found := origin[line.Offer]
		if !found || !sameLine(previous, line) {
			saved = append(saved, line)
		}
		delete(origin, line.Offer)
	}
	for id := range origin {
		removed = append(removed, id)
	}
	return saved, removed
}

func sameLine(previous, current Line) bool {
	return previous.Position == current.Position &&
		previous.Name == current.Name && previous.Quantity == current.Quantity &&
		previous.Price == current.Price && previous.Issue == current.Issue &&
		previous.AddedAt.Equal(current.AddedAt) && previous.CheckedAt.Equal(current.CheckedAt) &&
		slices.Equal(previous.Discounts, current.Discounts)
}

// ID — корзина.
func (c *Cart) ID() ids.UUID { return c.state.ID }

// Lines — строки по порядку добавления.
func (c *Cart) Lines() []Line { return c.State().Lines }

// Expired — корзину не трогали дольше срока.
func (c *Cart) Expired(now time.Time) bool { return !now.Before(c.state.ExpiresAt) }

// ResetIfExpired очищает просроченную корзину; возвращает, была ли очистка.
func (c *Cart) ResetIfExpired(now time.Time, ttl time.Duration) bool {
	if !c.Expired(now) {
		return false
	}

	c.state.Lines = nil
	c.state.Promos = nil
	c.touch(now, ttl)

	return true
}

// Add кладёт предложение; то же предложение складывает количества. Остаток
// сверяется с проекцией каталога — окончательно наличие подтверждает магазин.
func (c *Cart) Add(offer Offer, quantity int, now time.Time, ttl time.Duration) error {
	if quantity < 1 || quantity > MaxQuantity {
		return ErrInvalidQuantity
	}

	index := c.find(offer.ID)
	total := quantity
	if index >= 0 {
		total += c.state.Lines[index].Quantity
	}

	if err := fits(offer, total); err != nil {
		return err
	}

	if index < 0 {
		if len(c.state.Lines) >= MaxLines {
			return ErrTooManyLines
		}

		c.appendLine(offer.ID, now)
		index = len(c.state.Lines) - 1
	}

	line := &c.state.Lines[index]
	line.Name, line.Quantity, line.Price, line.Issue, line.CheckedAt = offer.Name, total, offer.Price, IssueNone, now
	c.touch(now, ttl)

	return nil
}

// SetQuantity задаёт количество строки; ноль удаляет строку.
func (c *Cart) SetQuantity(offer Offer, quantity int, now time.Time, ttl time.Duration) error {
	index := c.find(offer.ID)
	if index < 0 {
		return ErrLineNotFound
	}
	if quantity == 0 {
		c.Remove(offer.ID, now, ttl)

		return nil
	}
	if quantity < 0 || quantity > MaxQuantity {
		return ErrInvalidQuantity
	}
	if err := fits(offer, quantity); err != nil {
		return err
	}

	line := &c.state.Lines[index]
	line.Name, line.Quantity, line.Price, line.Issue, line.CheckedAt = offer.Name, quantity, offer.Price, IssueNone, now
	c.touch(now, ttl)

	return nil
}

// Remove убирает строку; отсутствующая строка — не изменение.
func (c *Cart) Remove(offerID ids.OfferID, now time.Time, ttl time.Duration) bool {
	index := c.find(offerID)
	if index < 0 {
		return false
	}

	tenant := c.state.Lines[index].Offer.Tenant
	c.state.Lines = append(c.state.Lines[:index], c.state.Lines[index+1:]...)
	found := false
	for _, line := range c.state.Lines {
		found = found || line.Offer.Tenant == tenant
	}
	c.resetPromo(tenant)
	if !found {
		delete(c.state.Promos, tenant)
	}
	c.touch(now, ttl)

	return true
}

// Apply переносит ответы магазинов: новая цена записывается в строку и
// помечается, нехватка и снятое с продажи — помечаются. Строки без ответа не
// трогаются. Возвращает, изменилось ли что-нибудь.
func (c *Cart) Apply(checks []Check, now time.Time, ttl time.Duration) bool {
	byOffer := make(map[ids.OfferID]Check, len(checks))
	for _, check := range checks {
		byOffer[check.Offer] = check
	}

	changed := false

	for i := range c.state.Lines {
		line := &c.state.Lines[i]

		check, ok := byOffer[line.Offer]
		if !ok {
			continue
		}

		issue := IssueNone
		switch {
		case !check.Exists:
			issue = IssueUnavailable
		case check.Available < line.Quantity:
			issue = IssueNotEnoughStock
		case check.Price.Cmp(line.Price) != 0:
			issue = IssuePriceChanged
		}

		if check.Exists && check.Price.Cmp(line.Price) != 0 {
			line.Price = check.Price
		}

		changed = changed || issue != line.Issue || issue == IssuePriceChanged
		line.Issue = issue
		line.CheckedAt = now
	}

	if changed {
		c.touch(now, ttl)
	}

	return changed
}

// Groups — строки по магазинам в порядке идентификаторов магазинов.
func (c *Cart) Groups() []Group {
	byTenant := map[ids.TenantID]*Group{}
	promos := ClonePromos(c.state.Promos)
	var order []ids.TenantID

	for _, line := range c.Lines() {
		group, ok := byTenant[line.Offer.Tenant]
		if !ok {
			group = &Group{TenantID: line.Offer.Tenant}
			if promo, ok := promos[line.Offer.Tenant]; ok {
				group.PromoCode = &promo
			}
			byTenant[line.Offer.Tenant] = group
			order = append(order, line.Offer.Tenant)
		}

		group.Lines = append(group.Lines, line)
	}

	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	groups := make([]Group, 0, len(order))
	for _, tenant := range order {
		groups = append(groups, *byTenant[tenant])
	}

	return groups
}

// Total — сумма строк по ценам снимка.
func (c *Cart) Total() (money.Money, error) {
	total := money.Money{}

	for _, line := range c.state.Lines {
		amount, err := LineAmount(line)
		if err != nil {
			return money.Money{}, err
		}

		if total, err = total.Add(amount); err != nil {
			return money.Money{}, err
		}
	}

	return total, nil
}

// LineAmount — цена строки, умноженная на количество, сложением через Money.
func LineAmount(line Line) (money.Money, error) {
	return DiscountedAmount(line.Price, line.Quantity, line.Discounts)
}

func fits(offer Offer, quantity int) error {
	if !offer.Visible {
		return ErrOfferUnavailable
	}
	if offer.Available < quantity {
		return ErrNotEnoughStock
	}

	return nil
}

// Позиции переживают удаление соседних строк, поэтому оно не требует их записи.
func (c *Cart) appendLine(offerID ids.OfferID, now time.Time) {
	var position int64
	if n := len(c.state.Lines); n != 0 {
		position = c.state.Lines[n-1].Position + 1
	}
	c.state.Lines = append(c.state.Lines, Line{Offer: offerID, AddedAt: now, Position: position})
}

func (c *Cart) find(offerID ids.OfferID) int {
	for i, line := range c.state.Lines {
		if line.Offer == offerID {
			return i
		}
	}

	return -1
}

func (c *Cart) touch(now time.Time, ttl time.Duration) {
	c.state.Version = c.originVersion + 1
	c.state.UpdatedAt = now
	c.state.ExpiresAt = now.Add(ttl)
}
