// Package colorpalette задаёт палитру таксономии и правила отображения оттенков.
package colorpalette

import (
	"encoding/json"
	"slices"
)

const (
	// Dictionary — словарь исходных значений цвета.
	Dictionary = "color"
)

var palette = []string{
	"Белый",
	"Чёрный",
	"Серый",
	"Серебристый",
	"Бежевый",
	"Коричневый",
	"Красный",
	"Бордовый",
	"Оранжевый",
	"Жёлтый",
	"Зелёный",
	"Голубой",
	"Синий",
	"Фиолетовый",
	"Розовый",
	"Золотистый",
	"Прозрачный",
	"Разноцветный",
}

// Values возвращает порядок палитры без возможности изменить общие данные.
func Values() []string {
	return slices.Clone(palette)
}

// IsBase проверяет принадлежность явно заданной палитре.
func IsBase(value string) bool {
	return slices.Contains(palette, value)
}

// Parents читает только доменную часть свободных метаданных реестра.
func Parents(raw json.RawMessage) ([]string, error) {
	var metadata struct {
		BaseColors []string `json:"baseColors"`
	}
	if len(raw) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, ErrInvalid.WithMessage("baseColors — массив строк")
	}
	return metadata.BaseColors, nil
}

// Validate проверяет родителей; подтверждённый оттенок обязан иметь привязку.
func Validate(value string, parents []string, confirmed bool) error {
	if len(parents) > 2 {
		return ErrInvalid.WithMessage("у оттенка не больше двух базовых цветов")
	}
	if IsBase(value) && len(parents) != 0 {
		return ErrInvalid.WithMessage("у базового цвета не может быть родителей")
	}
	if confirmed && !IsBase(value) && len(parents) == 0 {
		return ErrInvalid.WithMessage("цвет без базового")
	}
	seen := map[string]bool{}
	for _, parent := range parents {
		if !IsBase(parent) {
			return ErrInvalid.WithMessage("родитель не входит в базовую палитру: " + parent)
		}
		if seen[parent] {
			return ErrInvalid.WithMessage("базовый цвет повторяется: " + parent)
		}
		seen[parent] = true
	}
	return nil
}

// facetValues возвращает базовый цвет или родителей оттенка; без привязки значений нет.
func facetValues(value string, parents []string) []string {
	if IsBase(value) {
		return []string{value}
	}
	return slices.Clone(parents)
}
