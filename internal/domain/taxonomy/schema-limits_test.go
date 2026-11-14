package taxonomy_test

import (
	"fmt"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestSchemaFitsSearchDocument(t *testing.T) {
	d := definition()
	schema := taxonomy.Schema{}
	d.Attributes = nil
	for i := 0; i <= taxonomy.MaxSchemaAttributes; i++ {
		code := fmt.Sprintf("a%d", i)
		d.Attributes = append(d.Attributes, taxonomy.Attribute{Code: code, Name: code, Type: "string"})
		if i < taxonomy.MaxSchemaAttributes {
			schema.Attributes = append(schema.Attributes, code)
		}
	}
	d.Schemas["s1"] = schema
	if err := d.Validate(); err != nil {
		t.Fatalf("глобальных полей может быть больше, чем в одной схеме: %v", err)
	}
	schema.Attributes = append(schema.Attributes, d.Attributes[taxonomy.MaxSchemaAttributes].Code)
	d.Schemas["s1"] = schema
	if err := d.Validate(); err == nil {
		t.Fatal("схема длиннее поискового документа разрешена")
	}
}
