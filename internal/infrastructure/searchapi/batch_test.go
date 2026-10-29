package searchapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestBatchBoundsEncodedBytes(t *testing.T) {
	docs := []ports.OfferDocument{
		{
			Description: strings.Repeat("<", 400000),
		},
		{
			Description: strings.Repeat("<", 400000),
		},
	}
	batch, err := nextOfferBatch(docs)

	if err != nil || len(batch.Offers) != 1 {
		t.Fatalf("разбиение по байтам: count=%d err=%v", len(batch.Offers), err)
	}
	encoded, err := json.Marshal(batch)

	if err != nil || len(encoded) > requestLimit {
		t.Fatalf("размер JSON=%d err=%v", len(encoded), err)
	}
	_, err = nextOfferBatch([]ports.OfferDocument{
		{
			Description: strings.Repeat("<", requestLimit/6+1),
		},
	})

	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("принят слишком большой документ: %v", err)
	}
}
