package taxonomydata

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

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

func TestBundledGenderSearchTerms(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	gender, ok := release.Definition().Fields()["gender"]
	if !ok {
		t.Fatal("нет атрибута gender")
	}
	if len(gender.SearchTermsByValue) != 2 {
		t.Fatalf("выражения должны быть только у Мужской и Женский: %+v", gender.SearchTermsByValue)
	}
	for value, want := range map[string][]string{
		"Мужской":       {"мужчина"},
		"Женский":       {"женщина"},
		"Унисекс":       nil,
		"Для девочек":   nil,
		"Для мальчиков": nil,
	} {
		t.Run(value, func(t *testing.T) {
			if got := gender.SearchTermsByValue[value]; !slices.Equal(got, want) {
				t.Fatalf("выражения %q: %v, ожидаются %v", value, got, want)
			}
		})
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

func TestArtifactRejectsWrongPin(t *testing.T) {
	var lock releaseLock
	if err := json.Unmarshal(artifactLock, &lock); err != nil {
		t.Fatal(err)
	}
	changed := append(bytes.Clone(artifactBytes), '\n')
	if _, _, err := loadArtifact(changed, lock); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("изменение точных байтов: %v", err)
	}
	lock.Release++
	if _, _, err := loadArtifact(artifactBytes, lock); err == nil || !strings.Contains(err.Error(), "ожидался выпуск") {
		t.Fatalf("другой номер закреплённого выпуска: %v", err)
	}
}

func TestArtifactRejectsInvalidJSON(t *testing.T) {
	release := testArtifact(t).Manifest.Release
	for name, test := range map[string]struct {
		body []byte
		want string
	}{
		"поле корня": {
			body: bytes.Replace(artifactBytes, []byte("{"), []byte(`{"unexpected":true,`), 1),
			want: `unknown field "unexpected"`,
		},
		"поле атрибута": {
			body: bytes.Replace(artifactBytes, []byte(`"facet":`), []byte(`"unexpected":true,"facet":`), 1),
			want: `unknown field "unexpected"`,
		},
		"поле раскладки": {
			body: bytes.Replace(
				artifactBytes,
				[]byte(`"facetLayout": {`),
				[]byte(`"facetLayout": {"unexpected":true,`),
				1,
			),
			want: `unknown field "unexpected"`,
		},
		"старое закрепление": {
			body: bytes.Replace(artifactBytes, []byte(`"facet":`), []byte(`"facetPinned":true,"facet":`), 1),
			want: "недопустимый facetPinned",
		},
		"старое выключение": {
			body: bytes.Replace(
				artifactBytes,
				[]byte(`"facetOverrides":`),
				[]byte(`"hiddenFacets":[],"facetOverrides":`),
				1,
			),
			want: "недопустимый hiddenFacets",
		},
		"вычисляемое поле": {
			body: bytes.Replace(artifactBytes, []byte(`"facet":`), []byte(`"values":null,"facet":`), 1),
			want: "вычисляемое поле в исходнике",
		},
		"второй документ": {
			body: append(bytes.Clone(artifactBytes), []byte(`{}`)...),
			want: "лишние данные",
		},
		"версия формата": {
			body: bytes.Replace(artifactBytes, []byte(`"schemaVersion": 1`), []byte(`"schemaVersion": 2`), 1),
			want: "версия формата 2 не поддерживается",
		},
	} {
		t.Run(name, func(t *testing.T) {
			lock := releaseLock{Release: release, SHA256: fmt.Sprintf("%x", sha256.Sum256(test.body))}
			if _, _, err := loadArtifact(test.body, lock); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestArtifactRejectsInvalidDefinitions(t *testing.T) {
	for name, damage := range map[string]func(*artifact){
		"неполный охват": func(bundle *artifact) {
			bundle.Manifest.Coverage.Leaves++
		},
		"отсутствующая схема": func(bundle *artifact) {
			bundle.Categories[0].Children[0].Children[0].Schema = "missing"
		},
		"несуществующий атрибут": func(bundle *artifact) {
			key := "bag"
			schema := bundle.Schemas[key]
			schema.Optional = append(schema.Optional, "missing")
			bundle.Schemas[key] = schema
		},
		"неверная иконка корня": func(bundle *artifact) {
			bundle.Categories[0].Icon = "unknown-icon"
		},
		"иконка дочерней категории": func(bundle *artifact) {
			bundle.Categories[0].Children[0].Icon = bundle.Categories[0].Icon
		},
		"нет иконки корня": func(bundle *artifact) {
			bundle.Categories[0].Icon = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			bundle := testArtifact(t)
			damage(&bundle)
			if _, _, err := prepareArtifact(bundle, strings.Repeat("a", 64)); err == nil {
				t.Fatal("повреждённая поставка принята")
			}
		})
	}
}

func TestArtifactWithoutHistoricalChecksumUsesJSONDigest(t *testing.T) {
	bundle := testArtifact(t)
	bundle.Manifest.Checksum = ""
	bundle.Manifest.Release++
	body, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	lock := releaseLock{
		Release: bundle.Manifest.Release,
		SHA256:  fmt.Sprintf("%x", sha256.Sum256(body)),
	}
	release, _, err := loadArtifact(body, lock)
	if err != nil || release.Version != lock.Release || release.Checksum != lock.SHA256 {
		t.Fatalf("номер %d, checksum %s, ошибка %v", release.Version, release.Checksum, err)
	}
}

func testArtifact(t *testing.T) artifact {
	t.Helper()
	bundle, err := decodeArtifact(artifactBytes)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestBundledIdentifierFormats(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	fields := release.Definition().Fields()
	for code, format := range map[string]string{"gtin": "gtin", "book_isbn": "isbn"} {
		if fields[code].Format != format {
			t.Fatalf("атрибут %s: format = %q, ожидается %q", code, fields[code].Format, format)
		}
	}
}

func TestIdentityWarningsSourceRoundTrip(t *testing.T) {
	var source map[string]taxonomy.Schema
	raw := []byte(`{
		"base": {
			"abstract": true,
			"attributes": ["a1"],
			"identityWarnings": true,
			"identity": {"model": ["a1"], "axes": []}
		},
		"child": {"extends": "base", "attributes": [], "identityWarnings": false},
		"inherited": {"extends": "base", "attributes": []}
	}`)
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var restored map[string]taxonomy.Schema
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored["inherited"].IdentityWarnings != nil {
		t.Fatal("отсутствующее значение превратилось в явный false")
	}
	resolved, err := taxonomy.ExpandSchemas(restored)
	if err != nil {
		t.Fatal(err)
	}
	if resolved["child"].IdentityWarnings == nil || *resolved["child"].IdentityWarnings ||
		resolved["inherited"].IdentityWarnings == nil || !*resolved["inherited"].IdentityWarnings {
		t.Fatal("потерян явный false или наследуемый true при повторном разборе JSON")
	}
}

func TestRelease19RequiresBrandWithoutReplacingTree(t *testing.T) {
	bundle := testArtifact(t)
	release, coverage, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != 19 || bundle.CategoryMigration != nil || bundle.MigrationSource != nil ||
		release.ReplaceCategories || coverage.Categories != 3974 || coverage.Leaves != 3555 {
		t.Fatalf("параметры поставки 19: %+v", coverage)
	}
	var walk func([]taxonomy.Category)
	walk = func(nodes []taxonomy.Category) {
		for _, node := range nodes {
			if len(node.Children) == 0 {
				schema := release.Schemas[node.Schema]
				if !slices.Contains(schema.Attributes, "brand") || slices.Contains(schema.Optional, "brand") {
					t.Errorf("brand необязателен у %s", node.ID)
				}
			}
			walk(node.Children)
		}
	}
	walk(release.Categories)
}

func TestArtifactRejectsIncompleteCategoryMigration(t *testing.T) {
	release := taxonomy.Release{
		Version:           2,
		ReplaceCategories: true,
		Categories:        []taxonomy.Category{{ID: "c1"}, {ID: "c2"}},
	}
	for _, change := range []func(*categoryMigration){
		func(m *categoryMigration) { m.ToRelease++ },
		func(m *categoryMigration) { m.Mappings = m.Mappings[1:] },
		func(m *categoryMigration) { m.Mappings[0].To[0] = "missing" },
	} {
		bundle := artifact{
			MigrationSource: &migrationSource{
				Version:            1,
				artifactDefinition: artifactDefinition{Categories: release.Categories},
			},
			CategoryMigration: &categoryMigration{
				FromRelease: 1,
				ToRelease:   2,
				Mappings: []categoryMapping{
					{From: "c1", To: []string{"c1"}, Action: "unchanged", Reason: "точное совпадение"},
					{From: "c2", To: []string{"c2"}, Action: "unchanged", Reason: "точное совпадение"},
				},
			},
		}
		if err := validateArtifactMigration(bundle, release); err != nil {
			t.Fatal(err)
		}
		change(bundle.CategoryMigration)
		if err := validateArtifactMigration(bundle, release); err == nil {
			t.Fatal("принята неполная миграция категорий")
		}
	}
}
