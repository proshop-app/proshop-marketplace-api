package taxonomy

import (
	"errors"
	"strings"
	"testing"
)

func TestReleasePlans(t *testing.T) {
	release := Release{
		Version:  2,
		Checksum: strings.Repeat("a", 64),
	}
	latest := &ReleaseState{
		Version:          1,
		Checksum:         strings.Repeat("b", 64),
		PublishedVersion: 2,
	}
	tests := []struct {
		name    string
		current Definition
		latest  *ReleaseState
		release Release
		changed bool
		failure bool
	}{
		{
			name:    "первая поставка",
			current: Definition{Version: 0},
			release: release,
			changed: true,
		},
		{
			name:    "обновление без операторских правок",
			current: Definition{Version: 2},
			latest:  latest,
			release: release,
			changed: true,
		},
		{
			name:    "операторский снимок не перезаписывается",
			current: Definition{Version: 3},
			latest:  latest,
			release: release,
			failure: true,
		},
		{
			name:    "каталог без истории поставок",
			current: Definition{Version: 2},
			release: release,
			failure: true,
		},
		{
			name:    "повтор сохраняет операторские правки",
			current: Definition{Version: 3},
			latest:  latest,
			release: Release{Version: 1, Checksum: latest.Checksum},
		},
		{
			name:    "подмена содержимого версии",
			current: Definition{Version: 2},
			latest:  latest,
			release: Release{Version: 1, Checksum: release.Checksum},
			failure: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed, err := PlanRelease(test.current, test.latest, test.release)
			if changed != test.changed || errors.Is(err, ErrReleaseConflict) != test.failure {
				t.Fatalf("changed=%v, err=%v", changed, err)
			}
		})
	}
}

func TestTaxonomyRollbackPlans(t *testing.T) {
	release := Release{Version: 1, Checksum: strings.Repeat("a", 64)}
	state := &ReleaseState{
		Version:          1,
		Checksum:         release.Checksum,
		PublishedVersion: 2,
	}
	if changed, err := PlanTaxonomyRollback(Definition{Version: 2}, state, release); !changed || err != nil {
		t.Fatalf("первый откат: %v, %v", changed, err)
	}
	if _, err := PlanTaxonomyRollback(Definition{Version: 3}, state, release); err == nil {
		t.Fatal("откат не должен затирать правки оператора")
	}
	state.RolledBackVersion = 3
	if changed, err := PlanTaxonomyRollback(Definition{Version: 4}, state, release); changed || err != nil {
		t.Fatalf("повтор отката: %v, %v", changed, err)
	}
	if _, err := PlanRelease(Definition{Version: 3}, state, release); err == nil {
		t.Fatal("отменённая версия не должна применяться заново")
	}
	release.Version = 2
	if changed, err := PlanRelease(Definition{Version: 3}, state, release); !changed || err != nil {
		t.Fatalf("новая поставка после отката: %v, %v", changed, err)
	}
}

func TestCategoryIDDoesNotRelaxAttributeCode(t *testing.T) {
	definition := Definition{
		Attributes: []Attribute{{Code: "model", Name: "Модель", Type: "string"}},
		Schemas:    map[string]Schema{"s1": {Attributes: []string{"model"}, Optional: []string{"model"}}},
		Version:    1,
		Categories: []Category{
			{
				ID:     "group.product-type",
				Slug:   "group-product-type",
				Name:   "Категория",
				Schema: "s1",
			},
		},
	}
	if err := definition.Validate(); err != nil {
		t.Fatal(err)
	}
	definition.Attributes[0].Code = "unsafe.path"
	if err := definition.Validate(); err == nil {
		t.Fatal("код атрибута допускает путь")
	}
}
