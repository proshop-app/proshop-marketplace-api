package checkout_test

import (
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestSellerSubsidyFloor(t *testing.T) {
	group := checkout.Group{Goods: money.FromKopecks(1000)}
	cases := []struct {
		name    string
		subsidy participant.Subsidy
		percent int
		want    int64
	}{
		{
			name:    "настройка ниже порога",
			subsidy: participant.Subsidy{Enabled: true, Type: participant.SubsidyPercent, Value: 10},
			percent: 30,
			want:    3000,
		},
		{
			name:    "настройка выше порога",
			subsidy: participant.Subsidy{Enabled: true, Type: participant.SubsidyPercent, Value: 50},
			percent: 30,
			want:    5000,
		},
		{
			name: "потолок не опускает ниже порога",
			subsidy: participant.Subsidy{
				Enabled:   true,
				Type:      participant.SubsidyPercent,
				Value:     70,
				MaxAmount: money.FromKopecks(1000),
			},
			percent: 30,
			want:    3000,
		},
		{
			name:    "фиксированная сумма ниже порога",
			subsidy: participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed, Value: 1000},
			percent: 30,
			want:    3000,
		},
		{
			name:    "порог выключен",
			subsidy: participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed, Value: 1000},
			percent: 0,
			want:    1000,
		},
		{
			name:    "полная стоимость за счёт продавца",
			subsidy: participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed, Value: 1000},
			percent: 100,
			want:    10000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := group.DeliverySubsidy(
				money.FromKopecks(2000),
				tc.subsidy,
				money.FromKopecks(10000),
				tc.percent,
			)
			if err != nil || got.Kopecks() != tc.want {
				t.Fatalf("субсидия: %v, %v; ожидалось %d", got, err, tc.want)
			}
		})
	}
	for _, invalid := range []int{-1, 101} {
		_, err := group.DeliverySubsidy(
			money.FromKopecks(2000),
			participant.Subsidy{},
			money.FromKopecks(10000),
			invalid,
		)
		if !errors.Is(err, checkout.ErrInvalidSubsidyPercent) {
			t.Fatalf("порог %d: %v", invalid, err)
		}
	}
}
