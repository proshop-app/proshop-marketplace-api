package searchapi

import (
	"encoding/json"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestPublicPriceDocumentWire(t *testing.T) {
	for _, price := range []int64{0, 9007199254740993} {
		doc, err := toDocument(ports.OfferDocument{
			ID:        "t1~p1",
			TenantID:  "t1",
			ProductID: "p1",
			Visible:   true,
			Price:     price,
			BasePrice: price,
		})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["priceProfiles"] != nil || fields["price"] == nil || fields["basePrice"] == nil {
			t.Fatal(string(raw))
		}
		var actual int64
		if err := json.Unmarshal(fields["price"], &actual); err != nil || actual != price {
			t.Fatal(actual, err)
		}
	}
	if contractVersion != 12 {
		t.Fatal(contractVersion)
	}
}
