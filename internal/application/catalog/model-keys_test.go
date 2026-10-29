package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
)

type modelResolver struct {
	id    string
	err   error
	calls int
}
type modelObserver struct{ failures int }

func (r *modelResolver) Resolve(context.Context, string) (string, bool, error) {
	r.calls++
	return r.id, r.id != "", r.err
}
func (o *modelObserver) Conflict() {}

func (o *modelObserver) ResolveFailed() {
	o.failures++
}

func TestModelKeysPrecedenceFailureAndRollback(t *testing.T) {
	resolver := &modelResolver{id: "brand-1"}
	observer := &modelObserver{}
	service := NewModelKeys(resolver, observer, true)
	attrs := map[string]json.RawMessage{
		"gtin":        json.RawMessage(`"036000291452"`),
		"brand":       json.RawMessage(`"Some Brand"`),
		"part_number": json.RawMessage(`" AB-12 "`),
	}
	key, source := service.calculate(context.Background(), attrs)
	if key != "gtin:0036000291452" || source != "gtin" || resolver.calls != 0 {
		t.Fatal(key, source, resolver.calls)
	}
	delete(attrs, "gtin")
	key, source = service.calculate(context.Background(), attrs)
	if key != "mpn:brand-1:ab12" || source != "mpn" {
		t.Fatal(key, source)
	}
	resolver.id = "brand-2"
	key, _ = service.calculate(context.Background(), attrs)
	if key != "mpn:brand-2:ab12" {
		t.Fatal("слияние бренда не пересчитало ключ", key)
	}
	resolver.err = errors.New("registry unavailable")
	key, source = service.calculate(context.Background(), attrs)
	if key != "" || source != "" || observer.failures != 1 {
		t.Fatal(key, source, observer.failures)
	}
	product := catalog.RestoreProduct(catalog.ProductState{
		Version: 3,
		Card: catalog.Card{
			ModelKey:       "old",
			ModelKeySource: "mpn",
		},
	})
	changed := NewModelKeys(resolver, observer, false).Apply(context.Background(), product)
	if !changed || product.State().Card.ModelKey != "" {
		t.Fatal("отключение должно очищать вычисленный ключ")
	}
}
