package participant_test

import (
	"encoding/json"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func TestDeliveryISORegionsAndKnownCarriers(t *testing.T) {
	for _, test := range []struct {
		name     string
		regions  []string
		carriers []string
		valid    bool
	}{
		{"ISO", []string{"RU-MOW"}, []string{"cdek", "yandex"}, true},
		{"старая зона", []string{"cdek:81"}, []string{"cdek"}, false},
		{"выдуманный перевозчик", []string{"fake:abc"}, []string{"fake"}, false},
		{"неизвестный ISO", []string{"RU-XXX"}, []string{"cdek"}, false},
		{"пустые регионы", nil, []string{"cdek"}, false},
		{"пустые перевозчики", []string{"RU-MOW"}, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := participant.DeliverySettings{Regions: test.regions, Carriers: test.carriers}
			if (value.Validate() == nil) != test.valid {
				t.Fatalf("валидация: %+v", value)
			}
			if test.valid && (!value.Allows("yandex", "RU-MOW") || value.Allows("fake", "RU-MOW") ||
				value.Allows("cdek", "81") || value.Allows("cdek", "")) {
				t.Fatal("проверка покрытия", value)
			}
		})
	}
}

func TestDeliveryCloneNormalizesEmptyArraysAndCopiesContents(t *testing.T) {
	for _, original := range []participant.DeliverySettings{{}, {Regions: []string{}, Carriers: []string{}}} {
		cloned := original.Clone()
		encoded, err := json.Marshal(cloned)
		if err != nil || string(encoded) != `{"regions":[],"carriers":[]}` {
			t.Fatal(string(encoded), err)
		}
	}
	original := participant.DeliverySettings{Regions: []string{"RU-MOW"}, Carriers: []string{"cdek"}}
	cloned := original.Clone()
	cloned.Regions[0] = "RU-SPE"
	cloned.Carriers[0] = "yandex"
	if original.Regions[0] != "RU-MOW" || original.Carriers[0] != "cdek" {
		t.Fatal("клон изменил исходные массивы", original)
	}
}

func TestParticipationRequiresEveryMarketplaceCarrier(t *testing.T) {
	for _, carriers := range [][]string{{"cdek"}, {"yandex"}, {"cdek", "cdek"}, {"cdek", "other"}} {
		settings := participant.DeliverySettings{Regions: []string{"RU-MOW"}, Carriers: carriers}
		if settings.Validate() == nil || settings.Allows("cdek", "RU-MOW") || settings.Allows("yandex", "RU-MOW") {
			t.Fatal("неполный набор служб не разрешает участие", carriers)
		}
	}
	settings := participant.DeliverySettings{Regions: []string{"RU-MOW"}}
	for _, carrier := range delivery.Carriers() {
		settings.Carriers = append(settings.Carriers, string(carrier.Code))
	}
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, carrier := range delivery.Carriers() {
		if !settings.Allows(string(carrier.Code), "RU-MOW") {
			t.Fatal("каждая обязательная служба доступна", carrier.Code)
		}
	}
}
