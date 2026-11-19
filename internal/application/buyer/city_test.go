package buyer_test

import (
	"context"
	"testing"

	app "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestCitySuggestionsUseSharedSourceAndValidateBeforeCalling(t *testing.T) {
	source := &fake.Cities{
		Detection:   ports.DetectedCity{City: "Город", Country: "RU"},
		Suggestions: []ports.CitySuggestion{{City: "Город", Label: "Город, регион", FiasID: "city-id"}},
	}
	service := app.NewCities(source, source, fake.NewLimiter())
	ctx := context.Background()
	if _, err := service.Suggest(ctx, "192.0.2.1", "x"); err == nil || len(source.Values) != 0 {
		t.Fatal(err, source.Values)
	}
	result, err := service.Suggest(ctx, "192.0.2.1", " Город ")
	if err != nil || len(result) != 1 || result[0].FiasID != "city-id" || source.Values[0] != "Город" {
		t.Fatal(result, err)
	}
	detected, err := service.Detect(ctx, "192.0.2.1")
	if err != nil || detected.City != "Город" || source.IPs[0] != "192.0.2.1" {
		t.Fatal(detected, err)
	}
}
