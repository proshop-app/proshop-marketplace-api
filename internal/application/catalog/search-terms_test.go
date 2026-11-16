package catalog_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func TestDocumentSearchTermsOnlyForVisiblePublishedOffer(t *testing.T) {
	store := fake.NewStore()
	seedSeller(store, "t1", participant.TenantActive, true)
	seedProduct(store, "t1", "p1")
	var state ports.OfferState
	err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		var err error
		state, err = tx.Offers().Offer(ctx, "t1", "p1")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	original := state.Product.State()
	original.Card.Publication.Attributes = map[string]json.RawMessage{"a1": json.RawMessage(`true`)}
	original.Card.Publication.AttributeSearchTerms = map[string][]string{"a1": {"значение"}}
	original.Card.Publication.Attributes["color"] = json.RawMessage(`"Хаки"`)
	original.Card.Publication.AttributeFacetValues = map[string][]string{"color": {"Зелёный"}}
	for _, name := range []string{"visible", "hidden", "deleted", "unpublished"} {
		t.Run(name, func(t *testing.T) {
			product := original
			switch name {
			case "hidden":
				product.Card.Visible = false
			case "deleted":
				product.Deleted = true
			case "unpublished":
				product.Card.Publication.Published = false
			}
			state.Product = catalog.RestoreProduct(product)
			doc, ok := appcatalog.Document(state)
			if !ok {
				t.Fatal("нет документа для участника")
			}
			if name == "visible" {
				if !reflect.DeepEqual(doc.AttributeSearchTerms, original.Card.Publication.AttributeSearchTerms) {
					t.Fatalf("выражения не попали в предложение: %+v", doc)
				}
				if !reflect.DeepEqual(doc.AttributeFacetValues, original.Card.Publication.AttributeFacetValues) {
					t.Fatalf("значения фасета не попали в документ: %+v", doc)
				}
				return
			}
			if doc.Visible || len(doc.AttributeSearchTerms) != 0 || len(doc.Attributes) != 0 ||
				len(doc.AttributeFacetValues) != 0 {
				t.Fatalf("скрытое предложение сохраняет выражения или атрибуты: %+v", doc)
			}
		})
	}
}
