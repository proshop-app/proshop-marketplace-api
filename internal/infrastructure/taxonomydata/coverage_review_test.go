package taxonomydata

import (
	"slices"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestWorkwearAndSchoolMigrationKeepsGarmentPurpose(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := testArtifact(t).CategoryMigration
	mappings := map[string]categoryMapping{}
	for _, mapping := range migration.Mappings {
		mappings[mapping.From] = mapping
	}
	for _, audience := range []string{"women", "men"} {
		for _, garment := range []string{"jackets", "trousers"} {
			old := "clothing." + audience + "-work-" + garment
			if !slices.Contains(mappings[old].To, "clothing.work-"+garment) {
				t.Errorf("рабочий предмет потерял назначение: %s", old)
			}
		}
	}
	for _, id := range mappings["clothing.school-uniform"].To {
		if !strings.HasPrefix(id, "clothing.girls-school-") && !strings.HasPrefix(id, "clothing.boys-school-") {
			t.Errorf("школьная форма направлена в чужой ассортимент: %s", id)
		}
	}
	definition := release.Definition()
	for _, id := range []string{
		"clothing.work-trousers",
		"clothing.work-bib-overalls",
		"clothing.medical-coats",
		"clothing.girls-school-blouses",
		"clothing.boys-school-trousers",
	} {
		leaf, _, found := definition.Category(id)
		if !found {
			t.Fatalf("нет отдельного предмета формы: %s", id)
		}
		hasSize := false
		for _, field := range leaf.Attributes {
			if field.Code == "size_clothing_label" {
				hasSize = field.Required && field.Variant
			}
		}
		if !hasSize {
			t.Errorf("у предмета формы потеряна обязательная ось размера: %s", id)
		}
	}
}

func TestMotherhoodFieldsDoNotLeakThroughCommonClothing(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	definition := release.Definition()
	var walk func([]taxonomy.Category, bool)
	walk = func(nodes []taxonomy.Category, women bool) {
		for _, node := range nodes {
			inWomen := women || node.ID == "clothing.women"
			if len(node.Children) > 0 {
				walk(node.Children, inWomen)
				continue
			}
			leaf, _, _ := definition.Category(node.ID)
			for _, field := range leaf.Attributes {
				if field.Code != "maternity" && field.Code != "nursing_access" {
					continue
				}
				if !inWomen || field.Required {
					t.Errorf("неуместный или обязательный признак материнства: %s.%s", node.ID, field.Code)
				}
				if field.Code == "nursing_access" && strings.Contains(node.Schema, "lower_clothing") {
					t.Errorf("доступ к груди у поясного предмета: %s", node.ID)
				}
			}
		}
	}
	walk(release.Categories, false)
	for _, code := range []string{"maternity", "nursing_access"} {
		leaf, _, _ := definition.Category("clothing.women-shirts")
		if !slices.ContainsFunc(leaf.Attributes, func(field taxonomy.SchemaAttribute) bool {
			return field.Code == code
		}) {
			t.Errorf("нет применимого признака у женской рубашки: %s", code)
		}
	}
}

func TestNewPartsDoNotInheritForeignMeasurements(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	definition := release.Definition()
	for id, forbidden := range map[string][]string{
		"auto.car-inner-tubes":      {"tire_season", "tire_load_index", "tire_speed_index", "studded"},
		"auto.car-speakers":         {"voltage", "audio_output_w"},
		"outdoors.boat-propellers":  {"auto_make", "auto_model"},
		"computers.three-d-pens":    {"cable_length", "paper_format", "print_color"},
		"music.midi-controllers":    {"audio_channels", "cable_length"},
		"clothing.women-socks":      {"maternity", "nursing_access"},
		"clothing.women-underpants": {"nursing_access"},
	} {
		leaf, _, found := definition.Category(id)
		if !found {
			t.Fatal("потерян проверенный вид", id)
		}
		for _, field := range leaf.Attributes {
			if slices.Contains(forbidden, field.Code) {
				t.Errorf("чужое измерение детали: %s.%s", id, field.Code)
			}
		}
	}
	if _, _, found := definition.Category("garden.sauna-hats"); found {
		t.Error("возвращён второй адрес банной шапки")
	}
	if _, _, found := definition.Category("textiles.sauna-hats"); !found {
		t.Error("потерян канонический адрес банной шапки")
	}
}

func TestEnergyAndReservoirSchemasRequireSelectionParameters(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	definition := release.Definition()
	for id, codes := range map[string][]string{
		"electronics.portable-power-stations": {"battery_energy", "ac_output_w"},
		"kitchen.canning-autoclaves":          {"capacity_l"},
		"appliances.electric-blankets":        {"length", "width"},
		"appliances.electric-sheets":          {"length", "width"},
		"auto.car-speakers":                   {"speaker_impedance"},
		"auto.car-amplifiers":                 {"audio_output_w"},
	} {
		leaf, _, found := definition.Category(id)
		if !found {
			t.Fatal("потерян проверенный вид", id)
		}
		for _, code := range codes {
			if !slices.ContainsFunc(leaf.Attributes, func(field taxonomy.SchemaAttribute) bool {
				return field.Code == code && field.Required
			}) {
				t.Errorf("потерян необходимый параметр выбора: %s.%s", id, code)
			}
		}
	}
}

func TestNamedKindsRemainReachableFromGeneralRelease9Leaves(t *testing.T) {
	migration := testArtifact(t).CategoryMigration
	mappings := map[string]categoryMapping{}
	for _, mapping := range migration.Mappings {
		mappings[mapping.From] = mapping
	}
	for from, target := range map[string]string{
		"appliances.heaters":        "appliances.convectors",
		"kitchen.plates":            "kitchen.saucers",
		"food.ready-meals":          "food.main-dishes",
		"outdoors.inflatable-boats": "outdoors.kayaks",
	} {
		mapping := mappings[from]
		if mapping.Action != "split" || !slices.Contains(mapping.To, target) {
			t.Errorf("из общего листа потерян выделенный вид: %s → %s", from, target)
		}
	}
}
