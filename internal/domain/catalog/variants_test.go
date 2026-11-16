package catalog_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
)

func TestVariantFormsAndValidation(t *testing.T) {
	axis := `{"key":"platform.color","code":"color","name":"Цвет","value":"Синий"}`
	for _, body := range []string{
		`{"groupId":"g","axis":{"key":"platform.color","code":"color","name":"Цвет"},"value":"Синий"}`,
		`{"groupId":"g","axes":[` + axis + `]}`,
		`{"groupId":"g","axes":[` + axis + `],"extra":{"future":true}}`,
		`{"groupId":"g","axis":{"key":"platform.color","code":"color","name":"Цвет","extra":true},` +
			`"value":"Синий","extra":42}`,
		`{"groupId":"g","axes":[` + strings.Replace(axis, `"value":`, `"extra":true,"value":`, 1) + `]}`,
	} {
		var variants catalog.Variants
		if err := json.Unmarshal([]byte(body), &variants); err != nil {
			t.Fatal(err)
		}
		if err := variants.Validate("g"); err != nil {
			t.Fatal(err)
		}
		output, err := json.Marshal(variants)
		if err != nil || strings.Contains(string(output), `"axis":`) || len(variants.Axes) != 1 {
			t.Fatalf("только axes: %s %v", output, err)
		}
	}
	for _, axes := range []string{
		`[]`,
		`null`,
		`[` + axis + `,` + axis + `]`,
		`[` + strings.Replace(axis, "platform.color", "local", 1) + `]`,
		`[` + strings.Replace(axis, "Синий", " ", 1) + `]`,
		`[` + strings.Replace(axis, `"Синий"`, `["Синий"]`, 1) + `]`,
		`[` + strings.Replace(axis, "Синий", strings.Repeat("я", 1025), 1) + `]`,
	} {
		var variants catalog.Variants
		err := json.Unmarshal([]byte(`{"groupId":"g","axes":`+axes+`}`), &variants)
		if err == nil {
			err = variants.Validate("g")
		}
		if err == nil {
			t.Fatalf("приняты неверные оси: %s", axes)
		}
	}
	for _, axes := range []string{`[]`, `null`, `[` + axis + `]`} {
		var variants catalog.Variants
		body := `{"groupId":"g","axis":{},"value":"v","axes":` + axes + `}`
		if err := json.Unmarshal([]byte(body), &variants); err == nil {
			t.Fatal("приняты обе формы", body)
		}
	}
	for _, body := range []string{`{"axis":{}}`, `{"value":"Синий"}`} {
		var variants catalog.Variants
		if err := json.Unmarshal([]byte(body), &variants); err == nil {
			t.Fatal("принята неполная старая форма", body)
		}
	}
	for _, count := range []int{1, 2, 3, 8, 9} {
		variants := catalog.Variants{GroupID: "g"}
		for i := range count {
			code := fmt.Sprintf("axis%d", i)
			variants.Axes = append(variants.Axes, catalog.VariantAxis{
				Key:   "platform." + code,
				Code:  code,
				Name:  "Ось",
				Value: strings.Repeat("я", 1024),
			})
		}
		if valid := variants.Validate("g") == nil; valid != (count <= 8) {
			t.Fatalf("лимит осей: %d", count)
		}
	}
}
