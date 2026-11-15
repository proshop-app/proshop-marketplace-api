package ports

import "context"

// SellersCounts считает магазины по ключам, независимо от фильтров текущей выдачи.
type SellersCounts interface {
	CountSellers(context.Context, []string) (map[string]int, error)
}
