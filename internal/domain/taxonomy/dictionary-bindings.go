package taxonomy

import "slices"

// Ограничения правил и допустимые режимы источников подсказок.
const (
	MaxDictionaryBindings = 4096
	MaxHintDictionaries   = 16
	BindingExtend         = "extend"
	BindingReplace        = "replace"
)

// DictionaryBinding меняет только источники подсказок, не идентичность и тип поля.
type DictionaryBinding struct {
	CategoryID    string   `json:"categoryId,omitempty" yaml:"categoryId,omitempty"`
	AttributeCode string   `json:"attributeCode" yaml:"attributeCode"`
	Dictionaries  []string `json:"dictionaries" yaml:"dictionaries"`
	Mode          string   `json:"mode" yaml:"mode"`
}

type dictionaryBindingKey struct {
	category  string
	attribute string
}

type bindingScope struct {
	path       []string
	attributes map[string]bool
}

// HintDictionaries разрешает только путь выбранного узла, без правил его потомков.
// Порядок первого появления нужен для детерминизма и не задаёт приоритет выдачи.
func (d Definition) HintDictionaries(categoryID, attributeCode string) []string {
	attribute, exists := d.Fields()[attributeCode]
	if !exists || attribute.Type != "string" {
		return []string{}
	}
	var path []string
	if categoryID != "" {
		_, foundPath, found := d.Category(categoryID)
		if !found {
			return []string{}
		}
		path = foundPath
	}
	return hintDictionaries(attribute, path, d.dictionaryBindingIndex())
}

// SameDictionaryBinding сравнивает множества: перестановка не создаёт новую версию.
func SameDictionaryBinding(a, b DictionaryBinding) bool {
	if a.CategoryID != b.CategoryID || a.AttributeCode != b.AttributeCode || a.Mode != b.Mode ||
		len(a.Dictionaries) != len(b.Dictionaries) {
		return false
	}
	for _, code := range a.Dictionaries {
		if !slices.Contains(b.Dictionaries, code) {
			return false
		}
	}
	return true
}

func (d Definition) validateDictionaryBindings(fields map[string]Attribute) error {
	if len(d.DictionaryBindings) > MaxDictionaryBindings {
		return ErrInvalid.WithMessage("слишком много правил подсказок")
	}
	scopes := d.bindingScopes()
	used := map[string]bool{}
	for _, scope := range scopes {
		for code := range scope.attributes {
			used[code] = true
		}
	}
	index := map[dictionaryBindingKey]DictionaryBinding{}
	for _, binding := range d.DictionaryBindings {
		field, exists := fields[binding.AttributeCode]
		if !exists || field.Type != "string" || !used[binding.AttributeCode] {
			return ErrInvalid.WithMessage("подсказки допустимы только у используемого string атрибута")
		}
		if binding.CategoryID != "" {
			scope, exists := scopes[binding.CategoryID]
			if !exists || !scope.attributes[binding.AttributeCode] {
				return ErrInvalid.WithMessage("атрибут отсутствует в области категории")
			}
		}
		if binding.Dictionaries == nil || (binding.Mode != BindingExtend && binding.Mode != BindingReplace) ||
			len(binding.Dictionaries) > MaxHintDictionaries ||
			(binding.Mode == BindingExtend && len(binding.Dictionaries) == 0) {
			return ErrInvalid.WithMessage("неверный режим или число источников подсказок")
		}
		seen := map[string]bool{}
		for _, code := range binding.Dictionaries {
			if !ValidCode(code) || seen[code] {
				return ErrInvalid.WithMessage("неверный или повторный код источника подсказок")
			}
			seen[code] = true
		}
		key := dictionaryBindingKey{category: binding.CategoryID, attribute: binding.AttributeCode}
		if _, duplicate := index[key]; duplicate {
			return ErrInvalid.WithMessage("повтор правила категории и атрибута")
		}
		index[key] = binding
	}
	for code := range used {
		field := fields[code]
		if field.Type == "string" && len(hintDictionaries(field, nil, index)) > MaxHintDictionaries {
			return ErrInvalid.WithMessage("слишком много глобальных источников подсказок")
		}
	}
	for _, scope := range scopes {
		for code := range scope.attributes {
			field := fields[code]
			if field.Type == "string" && len(hintDictionaries(field, scope.path, index)) > MaxHintDictionaries {
				return ErrInvalid.WithMessage("слишком много итоговых источников подсказок")
			}
		}
	}
	return nil
}

func (d Definition) dictionaryBindingIndex() map[dictionaryBindingKey]DictionaryBinding {
	index := make(map[dictionaryBindingKey]DictionaryBinding, len(d.DictionaryBindings))
	for _, binding := range d.DictionaryBindings {
		index[dictionaryBindingKey{category: binding.CategoryID, attribute: binding.AttributeCode}] = binding
	}
	return index
}

func hintDictionaries(
	attribute Attribute,
	path []string,
	index map[dictionaryBindingKey]DictionaryBinding,
) []string {
	result := []string{}
	if attribute.Dictionary != "" {
		result = append(result, attribute.Dictionary)
	}
	scopes := append([]string{""}, path...)
	for _, category := range scopes {
		binding, found := index[dictionaryBindingKey{category: category, attribute: attribute.Code}]
		if !found {
			continue
		}
		if binding.Mode == BindingReplace {
			result = []string{}
		}
		for _, code := range binding.Dictionaries {
			if !slices.Contains(result, code) {
				result = append(result, code)
			}
		}
	}
	return result
}

func (d Definition) bindingScopes() map[string]bindingScope {
	scopes := map[string]bindingScope{}
	var walk func(Category, []string) map[string]bool
	walk = func(category Category, ancestors []string) map[string]bool {
		path := append(append([]string{}, ancestors...), category.ID)
		attributes := map[string]bool{}
		if schema, found := d.Schemas[category.Schema]; found {
			for _, code := range schema.Attributes {
				attributes[code] = true
			}
		}
		for _, child := range category.Children {
			for code := range walk(child, path) {
				attributes[code] = true
			}
		}
		scopes[category.ID] = bindingScope{path: path, attributes: attributes}
		return attributes
	}
	for _, root := range d.Categories {
		walk(root, nil)
	}
	return scopes
}
