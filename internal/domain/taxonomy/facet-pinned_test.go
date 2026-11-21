package taxonomy_test

import "testing"

func TestPinRequiresFacet(t *testing.T) {
	d := definition()
	pinned, unpinned := true, false
	d.Attributes[0].FacetPinned = &pinned
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	d.Attributes[0].Facet = false
	if err := d.Validate(); err == nil {
		t.Fatal("закреплён атрибут без фасета")
	}
	d.Attributes[0].FacetPinned = &unpinned
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}
