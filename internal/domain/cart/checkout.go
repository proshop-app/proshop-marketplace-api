package cart

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Consume убирает оформленное количество, сохраняя добавленное позже.
func (c *Cart) Consume(id ids.OfferID, quantity int, now time.Time, ttl time.Duration) bool {
	index := c.find(id)
	if index < 0 {
		return false
	}
	if c.state.Lines[index].Quantity <= quantity {
		return c.Remove(id, now, ttl)
	}
	c.state.Lines[index].Quantity -= quantity
	c.touch(now, ttl)
	return true
}

// Return восстанавливает снимок без доверия к остатку: перед оформлением нужна перепроверка.
func (c *Cart) Return(
	id ids.OfferID,
	name string,
	price money.Money,
	quantity int,
	now time.Time,
	ttl time.Duration,
) error {
	if quantity < 1 || quantity > MaxQuantity {
		return ErrInvalidQuantity
	}
	index := c.find(id)
	if index >= 0 && c.state.Lines[index].Quantity > MaxQuantity-quantity {
		return ErrInvalidQuantity
	}
	if index < 0 {
		c.state.Lines = append(c.state.Lines, Line{Offer: id, AddedAt: now})
		index = len(c.state.Lines) - 1
	}
	line := &c.state.Lines[index]
	line.Quantity += quantity
	line.Name, line.Price, line.Issue = name, price, IssuePriceChanged
	c.touch(now, ttl)
	return nil
}

// ReturnUpTo возвращает помещающееся количество; остаток остаётся в группе покупки.
func (c *Cart) ReturnUpTo(line Line, now time.Time, ttl time.Duration) (int, error) {
	index := c.find(line.Offer)
	capacity := MaxQuantity
	if index >= 0 {
		capacity -= c.state.Lines[index].Quantity
	}
	if index < 0 && len(c.state.Lines) >= MaxLines {
		return 0, ErrTooManyLines
	}
	quantity := line.Quantity
	if quantity > capacity {
		quantity = capacity
	}
	if quantity <= 0 {
		return 0, ErrInvalidQuantity
	}
	if err := c.Return(line.Offer, line.Name, line.Price, quantity, now, ttl); err != nil {
		return 0, err
	}
	if quantity < line.Quantity {
		return quantity, ErrInvalidQuantity
	}
	return quantity, nil
}
