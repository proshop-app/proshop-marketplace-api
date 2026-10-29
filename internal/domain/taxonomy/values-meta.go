package taxonomy

import (
	"regexp"
	"slices"
)

var colorHex = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func (a Attribute) validateValuesMeta() error {
	for value, meta := range a.ValuesMeta {
		if a.Type != "enum" || !slices.Contains(a.Values, value) {
			return ErrInvalid.WithMessage("метаданные относятся только к значению перечисления")
		}
		if (meta.Hex == "") == (meta.Swatch == "") ||
			(meta.Hex != "" && !colorHex.MatchString(meta.Hex)) ||
			(meta.Swatch != "" && meta.Swatch != "multicolor" && meta.Swatch != "pattern") {
			return ErrInvalid.WithMessage("некорректный образец цвета")
		}
	}

	return nil
}
