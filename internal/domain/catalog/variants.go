package catalog

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// MaxVariantAxes ограничивает размер контракта предложения.
const MaxVariantAxes = 8
const maxVariantValue = 1024

// VariantAxis — системная ось группы продавца.
type VariantAxis struct {
	Key   string `json:"key"`
	Code  string `json:"code"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Variants — положение предложения внутри группы своего магазина.
type Variants struct {
	GroupID  string        `json:"groupId"`
	Axes     []VariantAxis `json:"axes"`
	Position int           `json:"position"`
}

type variantsJSON Variants

// Validate проверяет согласованность группы и скалярных значений осей.
func (v *Variants) Validate(groupID string) error {
	if v == nil {
		return nil
	}
	if v.GroupID == "" || v.GroupID != groupID || v.Position < 0 || len(v.Axes) == 0 || len(v.Axes) > MaxVariantAxes {
		return ErrInvalidSnapshot.WithMessage("некорректные оси вариантов")
	}
	seen := make(map[string]bool, len(v.Axes))
	for _, axis := range v.Axes {
		if axis.Code == "" || axis.Key != "platform."+axis.Code || seen[axis.Key] ||
			strings.TrimSpace(axis.Name) == "" || strings.TrimSpace(axis.Value) == "" ||
			utf8.RuneCountInString(axis.Value) > maxVariantValue {
			return ErrInvalidSnapshot.WithMessage("некорректная ось вариантов")
		}
		seen[axis.Key] = true
	}
	return nil
}

// UnmarshalJSON нормализует сохранённые карточки и события магазина.
func (v *Variants) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}

	legacy := fields["axis"] != nil || fields["value"] != nil
	if legacy && (fields["axes"] != nil || fields["axis"] == nil || fields["value"] == nil) {
		return ErrInvalidSnapshot.WithMessage("обе формы вариантов или неполная старая форма")
	}

	decoded := Variants{}
	wire := struct {
		*variantsJSON
		Axis  VariantAxis `json:"axis"`
		Value string      `json:"value"`
	}{
		variantsJSON: (*variantsJSON)(&decoded),
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	if legacy {
		wire.Axis.Value = wire.Value
		decoded.Axes = []VariantAxis{wire.Axis}
	}

	*v = decoded
	return nil
}
