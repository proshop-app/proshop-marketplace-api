package taxonomy

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

var colorHex = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func (a Attribute) validateValuesMeta() error {
	for value, meta := range a.ValuesMeta {
		if !validOptionalText(value, 256) || value == "" ||
			(a.Type != "enum" && (a.Type != "string" || a.Dictionary == "" && !a.DictionarySelection)) ||
			(a.Type == "enum" && len(a.Values) > 0 && !slices.Contains(a.Values, value)) {
			return ErrInvalid.WithMessage("метаданные относятся только к справочному значению")
		}
		if err := meta.Validate(value); err != nil {
			return err
		}
		if meta.DisplayMeta() == nil {
			return ErrInvalid.WithMessage("отсутствуют метаданные значения")
		}
	}

	return nil
}

// Validate проверяет структуру метаданных из реестра или выпуска.
func (m ValueMeta) Validate(value string) error {
	if (m.Hex != "" && m.Swatch != "") || (m.Hex != "" && !colorHex.MatchString(m.Hex)) ||
		(m.Swatch != "" && m.Swatch != "multicolor" && m.Swatch != "pattern") {
		return ErrInvalid.WithMessage("некорректный образец цвета")
	}
	if !validOptionalText(m.SecondaryLabel, 120) {
		return ErrInvalid.WithMessage("некорректная дополнительная подпись")
	}
	if len(m.Covers) != 0 {
		if len(m.Covers) < 2 || slices.Contains(m.Covers, value) {
			return ErrInvalid.WithMessage("покрытие требует от 2 до 8 других значений")
		}
		for _, covered := range m.Covers {
			if utf8.RuneCountInString(covered) > 120 {
				return ErrInvalid.WithMessage("слишком длинное значение покрытия")
			}
		}
		return ValidateFacetValues(m.Covers)
	}
	return nil
}

func validOptionalText(value string, limit int) bool {
	return value == "" || strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= limit
}
