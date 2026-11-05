package taxonomydata

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"gopkg.in/yaml.v3"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestBundledTaxonomy(t *testing.T) {
	release, coverage, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := release.Definition().Validate(); err != nil {
		t.Fatal(err)
	}
	if coverage.Directions != len(release.Categories) || coverage.Leaves == 0 || coverage.Attributes == 0 {
		t.Fatalf("неполное покрытие: %+v", coverage)
	}
	for _, direction := range release.Categories {
		assertCategorySchemas(t, release.Definition(), direction)
	}
}

func assertCategorySchemas(t *testing.T, definition taxonomy.Definition, category taxonomy.Category) {
	t.Helper()
	if len(category.Children) == 0 {
		if len(definition.Schemas[category.Schema].Attributes) == 0 {
			t.Fatalf("нет схемы конечной категории %s", category.ID)
		}
		resolved, _, _ := definition.Category(category.ID)
		for _, field := range resolved.Attributes {
			if field.Code == "model" && field.Type != "string" {
				t.Fatalf("модели не должны требовать полного справочника: %s", category.ID)
			}
		}
	}
	for _, child := range category.Children {
		assertCategorySchemas(t, definition, child)
	}
}

func TestBundleRejectsDamage(t *testing.T) {
	tests := map[string]func(fstest.MapFS){
		"изменён файл без новой суммы": func(files fstest.MapFS) {
			files["data/categories.yaml"].Data = []byte("[]")
		},
		"неизвестное поле": func(files fstest.MapFS) {
			files["data/manifest.yaml"].Data = []byte(`{"unexpected":true}`)
		},
		"неполный охват": func(files fstest.MapFS) {
			var header Manifest
			_ = yaml.Unmarshal(files["data/manifest.yaml"].Data, &header)
			header.Coverage.Leaves++
			files["data/manifest.yaml"].Data, _ = yaml.Marshal(header)
		},
		"ссылка на отсутствующую схему": func(files fstest.MapFS) {
			var tree []taxonomy.Category
			_ = yaml.Unmarshal(files["data/categories.yaml"].Data, &tree)
			tree[0].Children[0].Children[0].Schema = "missing"
			files["data/categories.yaml"].Data, _ = yaml.Marshal(tree)
			updateChecksum(t, files)
		},
		"несуществующий обязательный атрибут": func(files fstest.MapFS) {
			var schemas map[string]taxonomy.Schema
			_ = yaml.Unmarshal(files["data/schemas.yaml"].Data, &schemas)
			for key, schema := range schemas {
				schema.Optional = append(schema.Optional, "missing")
				schemas[key] = schema
				break
			}
			files["data/schemas.yaml"].Data, _ = yaml.Marshal(schemas)
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
	var header Manifest
	if err := yaml.Unmarshal(files["data/manifest.yaml"].Data, &header); err != nil {
		t.Fatal(err)
	}
	checksum, err := bundleChecksum(files)
	if err != nil {
		t.Fatal(err)
	}
	header.Checksum = checksum
	files["data/manifest.yaml"].Data, _ = yaml.Marshal(header)
}

func TestManifestRequiresNewRelease(t *testing.T) {
	files := cloneBundle(t)
	current, err := PrepareManifest(files, 0, false)
	if err != nil || current.Release < 1 {
		t.Fatal(current, err)
	}
	files["data/attributes.yaml"].Data = append(files["data/attributes.yaml"].Data, []byte("\n# новая поставка\n")...)
	if _, err := PrepareManifest(files, 0, false); err == nil {
		t.Fatal("не замечено изменение байтов")
	}
	if _, err := PrepareManifest(files, current.Release, true); err == nil {
		t.Fatal("перезапись выпущенного номера")
	}
	next, err := PrepareManifest(files, current.Release+1, true)
	if err != nil || next.Checksum == current.Checksum || next.Coverage != current.Coverage {
		t.Fatal(next, err)
	}
	files["data/manifest.yaml"].Data, _ = yaml.Marshal(next)
	if _, _, err := load(files); err != nil {
		t.Fatal(err)
	}
}

func TestBundleRejectsYAMLReferencesAndExtraDocuments(t *testing.T) {
	for _, body := range []string{
		"- &a {code: a1, name: Поле, type: string}\n",
		"- &a {code: a1, name: Поле, type: string}\n- *a\n",
		"[]\n---\n[]\n",
		"- code: a1\n  name: Поле\n  type: string\n  values: [v1]\n",
	} {
		files := cloneBundle(t)
		files["data/attributes.yaml"].Data = []byte(body)
		updateChecksum(t, files)
		if _, _, err := load(files); err == nil {
			t.Fatal("принят запрещённый YAML", body)
		}
	}
}
