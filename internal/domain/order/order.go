// Package order — снимок покупки для кабинета. Источник статусов — заказы магазинов.
package order

import "github.com/proshop-app/proshop-marketplace/internal/domain/checkout"

// Purchase — согласованный снимок всех групп, треков и выбранного ПВЗ.
// Хранится в checkout и читается из той же транзакции, отдельной отстающей копии нет.
type Purchase struct{ state checkout.State }

// Snapshot создаёт независимый снимок покупки для кабинета.
func Snapshot(state checkout.State) Purchase {
	return Purchase{state: checkout.Restore(state).State()}
}

// State возвращает копию групп и выбранного ПВЗ.
func (p Purchase) State() checkout.State { return checkout.Restore(p.state).State() }
