package taxonomy_test

import "testing"

func TestPinRequiresFacet(t *testing.T) {
	d := definition()
	d.Attributes[0].FacetPinned = true
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	d.Attributes[0].Facet = false
	if err := d.Validate(); err == nil {
		t.Fatal("закреплён атрибут без фасета")
	}
}
