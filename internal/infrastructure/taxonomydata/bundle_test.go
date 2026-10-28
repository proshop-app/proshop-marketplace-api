package taxonomydata

import (
	"encoding/json"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestBundledTaxonomy(t *testing.T) {
	release, coverage, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := (taxonomy.Definition{Version: release.Version, Categories: release.Categories}).Validate(); err != nil {
		t.Fatal(err)
	}
	if coverage.Directions != len(release.Categories) || coverage.Leaves == 0 || coverage.Attributes == 0 {
		t.Fatalf("неполное покрытие: %+v", coverage)
	}
	for _, direction := range release.Categories {
		assertCategorySchemas(t, direction)
	}
}

func assertCategorySchemas(t *testing.T, category taxonomy.Category) {
	t.Helper()
	if len(category.Children) == 0 {
		if len(category.Attributes) == 0 {
			t.Fatalf("нет схемы конечной категории %s", category.ID)
		}
		for _, field := range category.Attributes {
			if field.Code == "model" && field.Type != "string" {
				t.Fatalf("модели не должны требовать полного справочника: %s", category.ID)
			}
		}
	}
	for _, child := range category.Children {
		assertCategorySchemas(t, child)
	}
}

func TestBundleRejectsDamage(t *testing.T) {
	tests := map[string]func(fstest.MapFS){
		"изменён файл без новой суммы": func(files fstest.MapFS) {
			files["data/categories.json"].Data = []byte("[]")
		},
		"неизвестное поле": func(files fstest.MapFS) {
			files["data/manifest.json"].Data = []byte(`{"unexpected":true}`)
		},
		"неполный охват": func(files fstest.MapFS) {
			var header manifest
			_ = json.Unmarshal(files["data/manifest.json"].Data, &header)
			header.Coverage.Leaves++
			files["data/manifest.json"].Data, _ = json.Marshal(header)
		},
		"ссылка на отсутствующую схему": func(files fstest.MapFS) {
			var tree []categorySource
			_ = json.Unmarshal(files["data/categories.json"].Data, &tree)
			tree[0].Children[0].Children[0].Schema = "missing"
			files["data/categories.json"].Data, _ = json.Marshal(tree)
			updateChecksum(t, files)
		},
		"несуществующий обязательный атрибут": func(files fstest.MapFS) {
			var schemas map[string]attributeSchema
			_ = json.Unmarshal(files["data/schemas.json"].Data, &schemas)
			for key, schema := range schemas {
				schema.Required = append(schema.Required, "missing")
				schemas[key] = schema
				break
			}
			files["data/schemas.json"].Data, _ = json.Marshal(schemas)
			updateChecksum(t, files)
		},
	}
	for name, damage := range tests {
		t.Run(name, func(t *testing.T) {
			files := cloneBundle(t)
			damage(files)
			if _, _, err := load(files); err == nil {
				t.Fatal("повреждённая поставка принята")
			}
		})
	}
}

func cloneBundle(t *testing.T) fstest.MapFS {
	t.Helper()
	files := make(fstest.MapFS)
	err := fs.WalkDir(bundled, "data", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := fs.ReadFile(bundled, path)
		if err != nil {
			return err
		}
		files[path] = &fstest.MapFile{Data: body}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return files
}

func updateChecksum(t *testing.T, files fstest.MapFS) {
	t.Helper()
	var header manifest
	if err := json.Unmarshal(files["data/manifest.json"].Data, &header); err != nil {
		t.Fatal(err)
	}
	checksum, err := bundleChecksum(files)
	if err != nil {
		t.Fatal(err)
	}
	header.Checksum = checksum
	files["data/manifest.json"].Data, _ = json.Marshal(header)
}
