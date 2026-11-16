package colorpalette

import (
	"encoding/json"
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
			name:    "нет образца корзины",
			message: "нет образца",
			mutate:  func(_ map[string]json.RawMessage, samples map[string]bool) { delete(samples, Other) },
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
	if err != nil || len(options) != 19 || options[18] != Other || values["Оттенок"][0] != Other {
		t.Fatalf("нет корзины для старого подтверждённого значения: %v %v %v", options, values, err)
	}
}

func paletteFixture() (map[string]json.RawMessage, map[string]bool) {
	entries := map[string]json.RawMessage{}
	samples := map[string]bool{Other: true}
	for _, value := range Values() {
		entries[value] = json.RawMessage(`{}`)
		samples[value] = true
	}
	return entries, samples
}
