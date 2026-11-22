package catalog

const (
	firstSale    = 1
	tenSales     = 10
	hundredSales = 100
)

func salesTier(count int64) uint8 {
	switch {
	case count >= hundredSales:
		return 3
	case count >= tenSales:
		return 2
	case count >= firstSale:
		return 1
	default:
		return 0
	}
}
