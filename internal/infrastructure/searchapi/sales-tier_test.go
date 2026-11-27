package searchapi

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestOfferBatchCarriesTierAndContractVersion(t *testing.T) {
	batch, err := nextOfferBatch([]ports.OfferDocument{
		{SalesTier: 0},
		{SalesTier: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(batch)
	if err != nil || batch.ContractVersion != 12 ||
		!bytes.Contains(raw, []byte(`"salesTier":0`)) || !bytes.Contains(raw, []byte(`"salesTier":3`)) ||
		bytes.Contains(raw, []byte(`"salesCount"`)) {
		t.Fatalf("неверный контракт ступеней продаж: %s, %v", raw, err)
	}
	if _, err := toDocument(ports.OfferDocument{SalesTier: 4}); err == nil {
		t.Fatal("принята ступень вне контракта")
	}
}
