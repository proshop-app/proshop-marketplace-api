package kafka

import (
	"encoding/json"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
)

func TestProjectionKeepsOnlyPublicChannelPrice(t *testing.T) {
	for _, test := range []struct {
		name     string
		profiles []priceProfileJSON
		found    bool
		invalid  bool
		price    int64
		base     int64
	}{
		{name: "нет цены"},
		{
			name: "чужие профили игнорируются",
			profiles: []priceProfileJSON{
				{Key: "storefront:default", Price: "bad", EffectivePrice: "bad"},
				{Key: "marketplace:group", Price: "-10", EffectivePrice: "-10"},
			},
		},
		{
			name: "ноль после акции",
			profiles: []priceProfileJSON{
				{Key: "marketplace:default", Price: "12.34", EffectivePrice: "0"},
			},
			found: true,
			base:  1234,
		},
		{
			name: "копейки без float",
			profiles: []priceProfileJSON{
				{Key: "marketplace:default", Price: "90071992547409.93", EffectivePrice: "1.005"},
			},
			found: true,
			price: 101,
			base:  9007199254740993,
		},
		{
			name: "дубли",
			profiles: []priceProfileJSON{
				{Key: "marketplace:default", Price: "1", EffectivePrice: "1"},
				{Key: "marketplace:default", Price: "2", EffectivePrice: "2"},
			},
			invalid: true,
		},
		{
			name: "отрицательная",
			profiles: []priceProfileJSON{
				{Key: "marketplace:default", Price: json.Number("-1"), EffectivePrice: "0"},
			},
			invalid: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var snapshot catalog.Snapshot
			err := snapshotPrices(&snapshot, test.profiles)
			if (err != nil) != test.invalid {
				t.Fatal(err)
			}
			if !test.invalid && (snapshot.HasPrice != test.found ||
				snapshot.Price.Kopecks() != test.price || snapshot.BasePrice.Kopecks() != test.base) {
				t.Fatal(snapshot)
			}
		})
	}
}
