package catalog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	app "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

func TestPanickingSharedRecommendationLoadClosesFlight(t *testing.T) {
	features := &fake.CatalogFeatures{RecommendHook: func(context.Context) error { panic("сбой отбора") }}
	store := fake.NewStore()
	queries := app.NewQueries(
		store,
		fake.NewIndex(),
		fake.NewPlatform(),
		features,
		features,
		fake.NewClock(time.Time{}),
	)
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		started := time.Now()
		_, err := queries.Recommend(ctx, app.RecommendationRequest{
			Context: app.RecommendationContextHome,
			Region:  "RU-MOW",
			Limit:   5,
		})
		cancel()
		if !errors.Is(err, errs.ErrPanic) || time.Since(started) >= time.Second {
			t.Fatalf("паника общего отбора закрывает рейс сразу: %v за %s", err, time.Since(started))
		}
	}
}
