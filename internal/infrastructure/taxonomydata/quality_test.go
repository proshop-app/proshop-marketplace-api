package taxonomydata

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

type exampleEntry struct {
	Canonical string `json:"canonical"`
	Status    string `json:"status"`
}

func TestEnumExamplesUseConfirmedValues(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range release.Attributes {
		if field.Type != "enum" {
			continue
		}
		dictionary := field.Dictionary
		// Новый пул отделяет возраст; пример взят из подтверждённого словаря происхождения.
		// Наличие нового closed-пула и его values проверяется публикацией через реестр.
		if field.Code == "target_gender" {
			dictionary = "gender"
		}
		raw, err := os.ReadFile("../../../deploy/reference/" + dictionary + "/entries.json")
		if err != nil {
			t.Fatal(err)
		}
		var entries []exampleEntry
		if err := json.Unmarshal(raw, &entries); err != nil {
			t.Fatal(err)
		}
		confirmed := false
		for _, entry := range entries {
			if entry.Canonical == field.Example && entry.Status == "confirmed" {
				confirmed = true
			}
		}
		if !confirmed {
			t.Errorf("пример %s=%q отсутствует в confirmed-значениях", field.Code, field.Example)
		}
	}
}

func TestTaxonomyQuality(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := auditRelease(release); err != nil {
		t.Fatal(err)
	}
}

func TestTaxonomyQualityRejectsAmbiguity(t *testing.T) {
	cases := map[string]func(*taxonomy.Release){
		"нет определения": func(release *taxonomy.Release) {
			release.Attributes[0].Description = " "
		},
		"нет примера": func(release *taxonomy.Release) {
			release.Attributes[0].Example = ""
		},
		"ветка унисекс": func(release *taxonomy.Release) {
			release.Categories[0].Children[0].Name = "Детям унисекс"
		},
		"единственная группа": func(release *taxonomy.Release) {
			release.Categories[0].Children[0].Children = release.Categories[0].Children[0].Children[:1]
		},
		"слишком длинный список": func(release *taxonomy.Release) {
			group := &release.Categories[0].Children[0]
			for len(group.Children) <= 25 {
				group.Children = append(group.Children, group.Children[0])
			}
		},
		"смешение осей": func(release *taxonomy.Release) {
			release.Categories[0].Children[0].Children[0].Axis = "gender"
		},
		"повтор листа во всём дереве": func(release *taxonomy.Release) {
			release.Categories[1].Children[0].Children[0].Name =
				release.Categories[0].Children[0].Children[0].Name
		},
		"нет причины отсутствия словаря": func(release *taxonomy.Release) {
			release.Attributes[1].DictionaryNote = ""
		},
		"размытое название": func(release *taxonomy.Release) {
			release.Attributes[0].Name = "Назначение"
		},
		"два смысла под двумя кодами": func(release *taxonomy.Release) {
			release.Attributes[1].Description = release.Attributes[0].Description
		},
		"повтор имени с ё и пробелами": func(release *taxonomy.Release) {
			release.Attributes[0].Name = "Ёмкость"
			release.Attributes[1].Name = " емкость "
		},
		"единица строки": func(release *taxonomy.Release) {
			release.Attributes[0].Unit = "см"
		},
		"единица в значении числа": func(release *taxonomy.Release) {
			for index := range release.Attributes {
				if release.Attributes[index].Type == "number" {
					release.Attributes[index].Example = "3 см"
					break
				}
			}
		},
		"словарь числа": func(release *taxonomy.Release) {
			release.Attributes[0].Type = "number"
			release.Attributes[0].Unit = "см"
		},
		"неподдерживаемый массив": func(release *taxonomy.Release) {
			release.Attributes[0].Multiple = true
		},
		"обязательная строка без исключения": func(release *taxonomy.Release) {
			schema := release.Schemas["bag"]
			schema.Optional = slices.DeleteFunc(schema.Optional, func(code string) bool {
				return code == "gtin"
			})
			release.Schemas["bag"] = schema
		},
		"повтор имени соседей": func(release *taxonomy.Release) {
			release.Categories[1].Name = "ЭлЕкТрОнИкА"
		},
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			release, _, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			damage(&release)
			if err := auditRelease(release); err == nil {
				t.Fatal("редакционный дефект не обнаружен")
			}
		})
	}
}

func TestRelease9AttributesAndCategoryMigration(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/release-9.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) !=
		"3389d0c4d0a06bd5cdd830537c8f9a79844f0c501b959fb7fc0874a35acc037e" {
		t.Fatal("изменён базовый снимок поставки 9")
	}
	var previous taxonomy.Definition
	if err := json.Unmarshal(raw, &previous); err != nil {
		t.Fatal(err)
	}
	if err := release.Definition().CheckCategoryReplacementEvolution(previous); err != nil {
		t.Fatal(err)
	}
	migration, err := migrationFrom(bundled, release)
	if err != nil || migration.FromRelease != 9 || migration.ToRelease != release.Version {
		t.Fatal(migration, err)
	}
	mappings := map[string]CategoryMapping{}
	for _, mapping := range migration.Mappings {
		mappings[mapping.From] = mapping
	}
	var walk func([]taxonomy.Category)
	walk = func(categories []taxonomy.Category) {
		for _, old := range categories {
			if len(old.Children) == 0 {
				if _, found := mappings[old.ID]; !found {
					t.Errorf("не учтён лист поставки 9: %s", old.ID)
				}
				delete(mappings, old.ID)
			}
			walk(old.Children)
		}
	}
	walk(previous.Categories)
	if len(mappings) != 0 {
		t.Fatal("лишние исходные коды миграции", mappings)
	}
}

func TestEveryYAMLPartFitsOperatorUploadLimit(t *testing.T) {
	entries, err := fs.ReadDir(bundled, "data")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, err := fs.ReadFile(bundled, "data/"+entry.Name())
		if err != nil || len(body) > 2*1024*1024 {
			t.Fatal("файл превышает лимит админки", entry.Name(), len(body), err)
		}
	}
}

func TestReviewedLeafSchemasDoNotReintroduceForeignObjects(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	definition := release.Definition()
	cases := map[string][]string{
		"accessories.key-rings":      {"bag_fastener", "bag_lock", "compatible_model"},
		"accessories.watch-straps":   {"power", "battery_capacity", "connector"},
		"accessories.watch-cases":    {"power", "compatible_model", "battery_capacity"},
		"accessories.men-cufflinks":  {"fabric_composition", "textile_material", "size_clothing_label"},
		"accessories.men-tie-clips":  {"fabric_composition", "textile_material"},
		"computers.mousepads":        {"power", "connector", "battery_capacity"},
		"computers.scanners":         {"print_color", "print_technology"},
		"computers.processors":       {"chipset"},
		"computers.usb-hubs":         {"supported_platform"},
		"shoes.shoe-horns":           {"compatible_model"},
		"accessories.travel-bottles": {"volume"},
	}
	for id, forbidden := range cases {
		leaf, _, found := definition.Category(id)
		if !found || len(leaf.Children) != 0 {
			t.Fatal("нет проверенного вида изделия", id)
		}
		for _, field := range leaf.Attributes {
			if slices.Contains(forbidden, field.Code) || field.Required && field.Code == "compatible_model" {
				t.Errorf("чужое или необоснованно обязательное поле %s.%s", id, field.Code)
			}
		}
	}
}

func TestSizeAxesRemainVariantAndStandardOptional(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	labels := []string{
		"size_clothing_label",
		"size_footwear_label",
		"headwear_size_label",
		"handwear_size_label",
		"ring_size_label",
		"diaper_size_label",
	}
	fields := release.Definition().Fields()
	for _, code := range labels {
		field, found := fields[code]
		if !found || !field.Variant || field.Dictionary == "" || field.Deprecated {
			t.Errorf("потеряна словарная ось размерной маркировки: %s", code)
		}
	}
	standard := fields["size_standard"]
	if standard.Variant || standard.Dictionary == "" {
		t.Fatal("система размера должна уточнять маркировку, не заменять ось варианта")
	}
	for name, schema := range release.Schemas {
		count := 0
		for _, code := range labels {
			if slices.Contains(schema.Attributes, code) {
				count++
			}
		}
		if count > 1 {
			t.Errorf("две маркировки размера одного изделия: %s", name)
		}
		if slices.Contains(schema.Attributes, "size_standard") && !slices.Contains(schema.Optional, "size_standard") {
			t.Errorf("необоснованно обязательная система размера: %s", name)
		}
	}
}
