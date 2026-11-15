package taxonomy

import (
	"encoding/json"
	"slices"
	"strings"
)

func (s Schema) validateIdentity(schema string, fields map[string]Attribute) error {
	if s.Identity == nil {
		return nil
	}
	if len(s.Identity.Model) == 0 {
		return ErrInvalid.WithMessage("схема " + schema + ": identity.model пуст")
	}
	seen := map[string]bool{}
	for index, codes := range [][]string{s.Identity.Model, s.Identity.Axes, s.Identity.Exact} {
		for _, code := range codes {
			prefix := "схема " + schema + ", атрибут " + code + ": "
			if !slices.Contains(s.Attributes, code) || seen[code] {
				return ErrInvalid.WithMessage(prefix + "неверная или повторная ссылка identity")
			}
			seen[code] = true
			if index < 2 && (s.IdentityWarnings == nil || !*s.IdentityWarnings) && slices.Contains(s.Optional, code) {
				return ErrInvalid.WithMessage(prefix + "identity требует обязательный атрибут")
			}
			kind := fields[code].Type
			if index == 1 && kind != "enum" && kind != "string" && kind != "number" {
				return ErrInvalid.WithMessage(prefix + "ось identity должна быть скалярной")
			}
		}
	}
	return nil
}

func (i *SchemaIdentity) contains(code string) bool {
	return i != nil && (slices.Contains(i.Model, code) || slices.Contains(i.Axes, code))
}

func (i *SchemaIdentity) issues(attributes map[string]json.RawMessage) []Issue {
	if i == nil {
		return nil
	}
	var issues []Issue
	for _, codes := range [][]string{i.Model, i.Axes} {
		for _, code := range codes {
			if identityEmpty(attributes[code]) {
				issues = append(issues, Issue{Code: "identity_incomplete", Attribute: code})
			}
		}
	}
	return issues
}

func identityEmpty(raw json.RawMessage) bool {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return true
	}
	var value string
	return json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) == ""
}
