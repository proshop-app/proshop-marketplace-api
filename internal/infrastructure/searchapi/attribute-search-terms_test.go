package searchapi

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestAttributeSearchTermsWireRoundTrip(t *testing.T) {
	doc := ports.OfferDocument{
		TenantID:  "t1",
		ProductID: "p1",
		Attributes: map[string]json.RawMessage{
			"a1": json.RawMessage(`"v1"`),
			"a2": json.RawMessage(`false`),
		},
		AttributeSearchTerms: map[string][]string{
			"a1": {"первый"},
			"a2": {"выключенный"},
			"a3": {"отсутствующий"},
		},
	}
	wire, err := toDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fromDocument(wire)
	want := map[string][]string{"a1": {"первый"}, "a2": {"выключенный"}}
	if err != nil || !reflect.DeepEqual(got.Attributes, doc.Attributes) ||
		!reflect.DeepEqual(got.AttributeSearchTerms, want) {
		t.Fatalf("выражения подменили исходные скаляры или добавили отсутствующий атрибут: %+v, %v", got, err)
	}
	wire.Attributes[0].SearchTerms[0] = "изменённый"
	if doc.AttributeSearchTerms["a1"][0] != "первый" || got.AttributeSearchTerms["a1"][0] != "первый" {
		t.Fatal("срез выражений не скопирован")
	}
	doc.AttributeSearchTerms = nil
	wire, err = toDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	got, err = fromDocument(wire)
	if err != nil || got.AttributeSearchTerms != nil {
		t.Fatalf("старый документ несовместим: %+v %v", got, err)
	}
}

func TestAttributeSearchTermsRejectInvalidProducerAndResponse(t *testing.T) {
	doc := ports.OfferDocument{
		Attributes:           map[string]json.RawMessage{"a1": json.RawMessage(`true`)},
		AttributeSearchTerms: map[string][]string{"a1": {"два  значения"}},
	}
	if _, err := toDocument(doc); !errors.Is(err, ErrInvalid) {
		t.Fatalf("некорректный запрос принят: %v", err)
	}
	wire := offerDocument{
		Attributes: []attribute{
			{Key: "a1", Type: "boolean", Value: "true", SearchTerms: []string{"Значение", "значение"}},
		},
	}
	if _, err := fromDocument(wire); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("некорректный ответ принят: %v", err)
	}
}
