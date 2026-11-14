package buyer_test

import (
	"context"
	"errors"
	"testing"

	app "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

func TestProfileKeepsUnchangedPickupWithoutLookup(t *testing.T) {
	ctx := context.Background()
	lookup := &fake.PickupLookup{Points: []delivery.PickupPoint{
		{Carrier: "cdek", Code: "p1", City: "Город", Address: "Адрес", RegionCode: "RU-AD"},
	}}
	service := app.NewFeatures(fake.NewStore(), nil, fake.NewPlatform(), fake.NewLimiter(), lookup)
	saved, err := service.Patch(ctx, featureBuyer, func(state buyer.ProfileState) (buyer.ProfileState, error) {
		state.PickupPoint = &buyer.Pickup{Provider: "cdek", Code: "p1", City: "Город"}
		return state, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Любое повторное обращение к источнику теперь вернёт «пункт не найден».
	lookup.Points = nil
	for _, city := range []string{"", "Город"} {
		result, err := service.Patch(ctx, featureBuyer, func(state buyer.ProfileState) (buyer.ProfileState, error) {
			state.PickupPoint = &buyer.Pickup{
				Provider:   "cdek",
				Code:       "p1",
				City:       city,
				Address:    "Подмена",
				RegionCode: "RU-AL",
			}
			return state, nil
		})
		if err != nil || result.PickupPoint == nil || *result.PickupPoint != *saved.PickupPoint ||
			result.Version != saved.Version {
			t.Fatal("сохранённый пункт изменился или был запрошен заново", result, err)
		}
	}
	for _, selection := range []buyer.Pickup{
		{Provider: "cdek", Code: "changed"},
		{Provider: "yandex", Code: "p1"},
	} {
		_, err := service.Patch(ctx, featureBuyer, func(state buyer.ProfileState) (buyer.ProfileState, error) {
			state.PickupPoint = &selection
			return state, nil
		})
		if !errors.Is(err, delivery.ErrInvalidCity) {
			t.Fatal("смена пункта без города принята", err)
		}
	}
	_, err = service.Patch(ctx, featureBuyer, func(state buyer.ProfileState) (buyer.ProfileState, error) {
		state.PickupPoint.City = "Другой город"
		return state, nil
	})
	if !errors.Is(err, delivery.ErrPickupPointNotFound) {
		t.Fatal("смена города обошла проверку", err)
	}
}
