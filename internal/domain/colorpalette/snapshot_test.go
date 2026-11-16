package colorpalette

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestSnapshotPolicyRejectsEachViolation(t *testing.T) {
	cases := []struct {
		name    string
		message string
		mutate  func(map[string]json.RawMessage, map[string]bool)
	}{
		{
			name:    "неподтверждённый базовый",
			message: "не подтверждён",
			mutate:  func(entries map[string]json.RawMessage, _ map[string]bool) { delete(entries, "Белый") },
		},
		{
			name:    "нет образца базового",
			message: "нет образца",
			mutate:  func(_ map[string]json.RawMessage, samples map[string]bool) { delete(samples, "Белый") },
		},
		{
			name:    "образец оттенка",
			message: "вне палитры",
			mutate:  func(_ map[string]json.RawMessage, samples map[string]bool) { samples["Хаки"] = true },
		},
		{
			name:    "неизвестный родитель",
			message: "не входит",
			mutate: func(entries map[string]json.RawMessage, _ map[string]bool) {
				entries["Хаки"] = json.RawMessage(`{"baseColors":["Оттенок"]}`)
			},
		},
		{
			name:    "три родителя",
			message: "не больше двух",
			mutate: func(entries map[string]json.RawMessage, _ map[string]bool) {
				entries["Хаки"] = json.RawMessage(`{"baseColors":["Белый","Синий","Зелёный"]}`)
			},
		},
		{
			name:    "родитель базового",
			message: "не может быть родителей",
			mutate: func(entries map[string]json.RawMessage, _ map[string]bool) {
				entries["Белый"] = json.RawMessage(`{"baseColors":["Синий"]}`)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries, samples := paletteFixture()
			tc.mutate(entries, samples)
			if _, _, err := Resolve(entries, samples); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("нет понятного отказа: %v", err)
			}
		})
	}
}

func TestSnapshotAllowsLegacyUnmappedValue(t *testing.T) {
	entries, samples := paletteFixture()
	entries["Оттенок"] = json.RawMessage(`{}`)
	options, values, err := Resolve(entries, samples)
	if err != nil || len(options) != 18 || !slices.Equal(options, Values()) {
		t.Fatalf("неверная палитра: %v %v", options, err)
	}
	if _, ok := values["Оттенок"]; ok {
		t.Fatalf("непривязанное значение получило фасет: %v", values)
	}
}

func paletteFixture() (map[string]json.RawMessage, map[string]bool) {
	entries := map[string]json.RawMessage{}
	samples := map[string]bool{}
	for _, value := range Values() {
		entries[value] = json.RawMessage(`{}`)
		samples[value] = true
	}
	return entries, samples
}
