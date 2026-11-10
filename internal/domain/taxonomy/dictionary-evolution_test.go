package taxonomy_test

import (
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestDictionaryEvolutionOnlyAddsStringHints(t *testing.T) {
	cases := []struct {
		name            string
		previous        taxonomy.Attribute
		next            taxonomy.Attribute
		allowed         bool
		rollbackAllowed bool
	}{
		{
			name:            "добавление подсказок",
			previous:        taxonomy.Attribute{Code: "a1", Type: "string"},
			next:            taxonomy.Attribute{Code: "a1", Type: "string", Dictionary: "a1"},
			allowed:         true,
			rollbackAllowed: true,
		},
		{
			name:            "сохранение словаря",
			previous:        taxonomy.Attribute{Code: "a1", Type: "string", Dictionary: "a1"},
			next:            taxonomy.Attribute{Code: "a1", Type: "string", Dictionary: "a1"},
			allowed:         true,
			rollbackAllowed: true,
		},
		{
			name:            "удаление только при откате",
			previous:        taxonomy.Attribute{Code: "a1", Type: "string", Dictionary: "a1"},
			next:            taxonomy.Attribute{Code: "a1", Type: "string"},
			rollbackAllowed: true,
		},
		{
			name:     "смена словаря строки",
			previous: taxonomy.Attribute{Code: "a1", Type: "string", Dictionary: "a1"},
			next:     taxonomy.Attribute{Code: "a1", Type: "string", Dictionary: "a2"},
		},
		{
			name:     "смена словаря enum",
			previous: taxonomy.Attribute{Code: "a1", Type: "enum", Dictionary: "a1"},
			next:     taxonomy.Attribute{Code: "a1", Type: "enum", Dictionary: "a2"},
		},
		{
			name:     "удаление словаря enum",
			previous: taxonomy.Attribute{Code: "a1", Type: "enum", Dictionary: "a1"},
			next:     taxonomy.Attribute{Code: "a1", Type: "enum"},
		},
		{
			name:     "строка не становится enum",
			previous: taxonomy.Attribute{Code: "a1", Type: "string"},
			next:     taxonomy.Attribute{Code: "a1", Type: "enum", Dictionary: "a1"},
		},
		{
			name:     "единица не меняется",
			previous: taxonomy.Attribute{Code: "a1", Type: "number", Unit: "u1"},
			next:     taxonomy.Attribute{Code: "a1", Type: "number", Unit: "u2"},
		},
		{
			name:     "код не удаляется",
			previous: taxonomy.Attribute{Code: "a1", Type: "string"},
			next:     taxonomy.Attribute{Code: "a2", Type: "string"},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			old := taxonomy.Definition{Attributes: []taxonomy.Attribute{test.previous}}
			next := taxonomy.Definition{Attributes: []taxonomy.Attribute{test.next}}
			for _, rollback := range []bool{false, true} {
				allowed := test.allowed
				check := next.CheckEvolution
				if rollback {
					allowed = test.rollbackAllowed
					check = next.CheckRollbackEvolution
				}
				err := check(old)
				if (allowed && err != nil) || (!allowed && !errors.Is(err, taxonomy.ErrConflict)) {
					t.Fatalf("rollback=%t allowed=%t error=%v", rollback, allowed, err)
				}
			}
		})
	}
}

func TestStringHintBindingPreservesRawCardAndUnknownValues(t *testing.T) {
	old := definition()
	next := definition()
	next.Attributes[3].Dictionary = "a4"
	if err := next.CheckEvolution(old); err != nil {
		t.Fatal(err)
	}
	input := card()
	before := old.Check(input)
	after := next.Check(input)
	if !before.Published || !after.Published || string(after.Attributes["a4"]) != string(before.Attributes["a4"]) {
		t.Fatal("привязка подсказок изменила неизвестную строку", before, after)
	}
}
