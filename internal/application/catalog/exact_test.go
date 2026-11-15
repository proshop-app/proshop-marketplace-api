package catalog_test

import (
	"context"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestSearchPreservesExact(t *testing.T) {
	index := &fake.PromotionIndex{}
	queries := appcatalog.NewQueries(
		fake.NewStore(),
		index,
		fake.NewPlatform(),
		nil,
		nil,
		fake.NewClock(time.Time{}),
	)

	for _, exact := range []bool{true, false, true} {
		_, err := queries.Search(context.Background(), ports.OfferQuery{
			Text:  " товра ",
			Exact: exact,
		})
		if err != nil {
			t.Fatal(err)
		}
		if index.Query.Exact != exact || index.Query.Text != "товра" {
			t.Fatalf("нормализация потеряла режим или исправила текст: %+v", index.Query)
		}
		if index.Query.Limit != appcatalog.DefaultLimit || index.Query.Sort != ports.SortRelevance {
			t.Fatalf("изменены прежние умолчания поиска: %+v", index.Query)
		}
	}
}
