package taxonomy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestFacetMappingUsesOnlyExactValue(t *testing.T) {
	field := Attribute{
		Code:               "a1",
		Type:               "enum",
		Values:             []string{"v1"},
		FacetOptions:       []string{"f1", "f2"},
		FacetValuesByValue: map[string][]string{"v1": {"f1", "f2"}},
	}
	if !reflect.DeepEqual(field.facetValues(json.RawMessage(`"v1"`)), []string{"f1", "f2"}) {
		t.Fatal("потеряно отображение значения")
	}
	for _, value := range []string{`"V1"`, `"v2"`} {
		if field.check(json.RawMessage(value)) != "taxonomy.enum_value" {
			t.Fatal("принято значение вне enum", value)
		}
	}
}

func TestFacetValuesContract(t *testing.T) {
	cases := [][]string{
		nil,
		{strings.Repeat("я", 1025)},
		{},
		{""},
		{" "},
		{"v1", "v1"},
		{"1", "2", "3", "4", "5", "6", "7", "8", "9"},
	}
	for _, values := range cases {
		if err := ValidateFacetValues(values); err == nil {
			t.Errorf("приняты неверные значения %v", values)
		}
	}
	if err := ValidateFacetValues([]string{"v1", "v2"}); err != nil {
		t.Fatal(err)
	}
}

func TestFacetValueLengthUsesRunes(t *testing.T) {
	if err := ValidateFacetValues([]string{strings.Repeat("я", 1024)}); err != nil {
		t.Fatal(err)
	}
}
