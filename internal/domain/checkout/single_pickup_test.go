package checkout_test

import (
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

func TestCommonCarriersIncludeEveryRemainingShop(t *testing.T) {
	for _, example := range []struct {
		name   string
		groups []checkout.Group
		want   []string
	}{
		{
			name: "intersection",
			groups: []checkout.Group{
				{Carriers: []string{"cdek", "yandex"}},
				{Carriers: []string{"yandex"}},
			},
			want: []string{"yandex"},
		},
		{
			name: "no shared carrier",
			groups: []checkout.Group{
				{Carriers: []string{"cdek"}},
				{Carriers: []string{"yandex"}},
			},
			want: []string{},
		},
		{
			name: "removed shop",
			groups: []checkout.Group{
				{Carriers: []string{"cdek"}},
				{Carriers: []string{"yandex"}, Abandoned: true},
			},
			want: []string{"cdek"},
		},
		{name: "empty purchase", want: []string{}},
	} {
		t.Run(example.name, func(t *testing.T) {
			purchase := checkout.Restore(checkout.State{Groups: example.groups})
			if actual := purchase.CommonCarriers(); !reflect.DeepEqual(actual, example.want) {
				t.Fatal(actual, example.want)
			}
		})
	}
}
