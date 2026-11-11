package taxonomy

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

// Ограничения выражений одинаковы в таксономии и служебном контракте поиска.
const (
	MaxValueSearchTerms = 8
	MaxSearchTermRunes  = 128
)

// Выражения принадлежат определённому значению атрибута. Они не меняют
// допустимые значения, не становятся глобальными синонимами или фасетами.
func (a Attribute) validateSearchTerms() error {
	if len(a.SearchTermsByValue) == 0 {
		return nil
	}
	if a.Type != "enum" && a.Type != "boolean" {
		return ErrInvalid.WithMessage("поисковые выражения допустимы только для enum и boolean")
	}
	if len(a.SearchTermsByValue) > 256 {
		return ErrInvalid.WithMessage("слишком много значений с поисковыми выражениями")
	}
	for value, terms := range a.SearchTermsByValue {
		valid := a.Type == "boolean" && (value == "true" || value == "false")
		if a.Type == "enum" {
			valid = strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= 256 &&
				(len(a.Values) == 0 || slices.Contains(a.Values, value))
		}
		if !valid || terms == nil {
			return ErrInvalid.WithMessage("неверное значение или число поисковых выражений")
		}
		if err := ValidateSearchTerms(terms); err != nil {
			return err
		}
	}

	return nil
}

// ValidateSearchTerms проверяет общий контракт выражений перед отправкой в поиск.
func ValidateSearchTerms(terms []string) error {
	if len(terms) > MaxValueSearchTerms {
		return ErrInvalid.WithMessage("слишком много поисковых выражений")
	}
	seen := make(map[string]bool, len(terms))
	for _, term := range terms {
		key := strings.ReplaceAll(strings.ToLower(term), "ё", "е")
		if term == "" || term != reference.Collapse(term) || !utf8.ValidString(term) ||
			utf8.RuneCountInString(term) > MaxSearchTermRunes || strings.ContainsFunc(term, unicode.IsControl) ||
			seen[key] {
			return ErrInvalid.WithMessage("пустое, длинное, повторное или некорректное поисковое выражение")
		}
		seen[key] = true
	}
	return nil
}

// Вызывается только после проверки скаляра схемой: строка "true" не может
// воспользоваться выражениями логического значения true.
func (a Attribute) searchTerms(raw json.RawMessage) []string {
	value := strings.TrimSpace(string(raw))
	if a.Type == "enum" {
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil
		}
	}

	return slices.Clone(a.SearchTermsByValue[value])
}
