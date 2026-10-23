package checkout

import (
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// DeliverySubsidy — часть перевозки, которую оплачивает этот продавец.
// Суммы других групп и деньги платформы в расчёте не участвуют.
func (g Group) DeliverySubsidy(
	freeDeliveryFrom money.Money,
	subsidy participant.Subsidy,
	cost money.Money,
	minPercent int,
) (money.Money, error) {
	if minPercent < 0 || minPercent > 100 {
		return money.Money{}, ErrInvalidSubsidyPercent
	}
	if g.Goods.Cmp(freeDeliveryFrom) >= 0 {
		return cost, nil
	}
	configured, err := subsidy.Apply(cost)
	if err != nil {
		return money.Money{}, err
	}
	minimum, err := cost.MulRatio(int64(minPercent), 100)
	if err != nil {
		return money.Money{}, err
	}
	if configured.Cmp(minimum) < 0 {
		return minimum, nil
	}
	return configured, nil
}
