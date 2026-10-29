package catalog

import "strings"

// VariantAxis — единственная системная ось группы продавца.
type VariantAxis struct {
	Key  string `json:"key"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// Variants — положение предложения внутри группы своего магазина.
type Variants struct {
	GroupID  string      `json:"groupId"`
	Axis     VariantAxis `json:"axis"`
	Value    string      `json:"value"`
	Position int         `json:"position"`
}

// Validate проверяет согласованность группы и скалярного значения оси.
func (v *Variants) Validate(groupID string) error {
	if v == nil {
		return nil
	}
	if v.GroupID == "" || v.GroupID != groupID || v.Position < 0 ||
		v.Axis.Code == "" || v.Axis.Key != "platform."+v.Axis.Code ||
		strings.TrimSpace(v.Axis.Name) == "" || strings.TrimSpace(v.Value) == "" {
		return ErrInvalidSnapshot.WithMessage("некорректная ось вариантов")
	}
	return nil
}
