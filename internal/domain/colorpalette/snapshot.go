package colorpalette

import "encoding/json"

// Resolve проверяет подтверждённые записи и образцы перед публикацией снимка.
// Непривязанные старые записи допустимы только здесь: новые подтверждения проверяет реестр.
func Resolve(
	confirmed map[string]json.RawMessage,
	samples map[string]bool,
) ([]string, map[string][]string, error) {
	for _, value := range palette {
		if _, ok := confirmed[value]; !ok {
			return nil, nil, ErrInvalid.WithMessage("базовый цвет не подтверждён в реестре: " + value)
		}
	}
	options := append(Values(), Other)
	for _, value := range options {
		if !samples[value] {
			return nil, nil, ErrInvalid.WithMessage("нет образца значения фасета: " + value)
		}
	}
	for value := range samples {
		if !IsBase(value) && value != Other {
			return nil, nil, ErrInvalid.WithMessage("образец задан вне палитры: " + value)
		}
	}
	byValue := make(map[string][]string, len(confirmed))
	for value, metadata := range confirmed {
		parents, err := Parents(metadata)
		if err != nil {
			return nil, nil, err
		}
		if err := Validate(value, parents, false); err != nil {
			return nil, nil, err
		}
		byValue[value] = facetValues(value, parents)
	}
	return options, byValue, nil
}
