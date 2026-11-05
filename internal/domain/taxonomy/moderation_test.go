package taxonomy_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestModerationInheritance(t *testing.T) {
	d := taxonomy.Definition{
		Version:    1,
		Attributes: []taxonomy.Attribute{{Code: "a1", Name: "Атрибут", Type: "string"}},
		Schemas:    map[string]taxonomy.Schema{"s1": {Attributes: []string{"a1"}}},
		Categories: []taxonomy.Category{{
			ID:         "c1",
			Name:       "Категория",
			Slug:       "c1",
			Moderation: "manual",
			Children: []taxonomy.Category{
				{ID: "c2", Name: "Категория 2", Slug: "c2", Schema: "s1"},
				{ID: "c3", Name: "Категория 3", Slug: "c3", Schema: "s1", Moderation: "auto"},
			},
		}},
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"c1": "manual", "c2": "manual", "c3": "auto", "c4": "auto"} {
		if got := d.ModerationPolicy(id); got != want {
			t.Fatalf("%s: %s вместо %s", id, got, want)
		}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var copyDefinition taxonomy.Definition
	if err := json.Unmarshal(raw, &copyDefinition); err != nil {
		t.Fatal(err)
	}
	if copyDefinition.ModerationPolicy("c2") != "manual" {
		t.Fatal("снимок потерял политику")
	}
	d.Categories[0].Moderation = "unknown"
	if err := d.Validate(); !errors.Is(err, taxonomy.ErrInvalid) {
		t.Fatal(err)
	}
}
