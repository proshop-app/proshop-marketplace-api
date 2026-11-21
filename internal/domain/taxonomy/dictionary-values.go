package taxonomy

import "slices"

// DictionaryValues фиксирует только подтверждённые канонические значения.
type DictionaryValues struct {
	ValuesMeta         map[string]ValueMeta `json:"valuesMeta"`
	SearchTermsByValue map[string][]string  `json:"searchTermsByValue,omitempty"`
}

// BuyerName возвращает короткое имя характеристики для витрины.
func (a Attribute) BuyerName() string {
	if a.DisplayName != "" {
		return a.DisplayName
	}
	return a.Name
}

// DisplayMeta исключает пустые метаданные подтверждённой записи.
func (m ValueMeta) DisplayMeta() *ValueMeta {
	if m.Hex == "" && m.Swatch == "" && m.SecondaryLabel == "" && len(m.Covers) == 0 && m.Order == nil {
		return nil
	}
	m.Covers = slices.Clone(m.Covers)
	if m.Order != nil {
		order := *m.Order
		m.Order = &order
	}
	return &m
}

func (a Attribute) selectValues(values DictionaryValues) Attribute {
	a.ValuesMeta = values.ValuesMeta
	a.dictionarySearchTerms = values.SearchTermsByValue
	return a
}

func (d Definition) selectCategoryFields(
	category *CategorySchema,
	path []string,
	bindings map[dictionaryBindingKey]DictionaryBinding,
) {
	for i, field := range category.Attributes {
		if field.DictionarySelection {
			dictionaries := hintDictionaries(field.Attribute, path, bindings)
			if len(dictionaries) == 1 {
				category.Attributes[i].Attribute = field.selectValues(d.SelectionValues[dictionaries[0]])
			}
		}
	}
}

// CatalogAttributes раскрывает глобальный источник. Для раздела нужен Category.
func (d Definition) CatalogAttributes() []Attribute {
	out := slices.Clone(d.Attributes)
	index := d.dictionaryBindingIndex()
	for i, a := range out {
		if a.DictionarySelection {
			dictionaries := hintDictionaries(a, nil, index)
			if len(dictionaries) == 1 {
				out[i] = a.selectValues(d.SelectionValues[dictionaries[0]])
			}
		}
	}
	return out
}

// SelectionSources возвращает сетки разделов для фасетов и карточек.
func (d Definition) SelectionSources() map[string]map[string]string {
	out := map[string]map[string]string{}
	fields, bindings := d.Fields(), d.dictionaryBindingIndex()
	for category, scope := range d.bindingScopes() {
		for code := range scope.attributes {
			field := fields[code]
			if !field.DictionarySelection {
				continue
			}
			sources := hintDictionaries(field, scope.path, bindings)
			if len(sources) == 1 {
				if out[category] == nil {
					out[category] = map[string]string{}
				}
				out[category][code] = sources[0]
			}
		}
	}
	return out
}

// SelectionDictionaries собирает источники выбора для разделов и глобального списка.
func (d Definition) SelectionDictionaries() []string {
	fields, bindings := d.Fields(), d.dictionaryBindingIndex()
	codes := map[string]bool{}
	for _, field := range fields {
		if field.DictionarySelection {
			for _, code := range hintDictionaries(field, nil, bindings) {
				codes[code] = true
			}
		}
	}
	for _, scope := range d.bindingScopes() {
		for code := range scope.attributes {
			field := fields[code]
			if !field.DictionarySelection {
				continue
			}
			for _, dictionary := range hintDictionaries(field, scope.path, bindings) {
				codes[dictionary] = true
			}
		}
	}
	out := make([]string, 0, len(codes))
	for code := range codes {
		out = append(out, code)
	}
	slices.Sort(out)
	return out
}

// SharedValueMeta разрешает подпись только при совпадении во всех применимых сетках,
// в которых есть это значение.
func (d Definition) SharedValueMeta(dictionaries []string, value string) ValueMeta {
	var result ValueMeta
	found := false
	for _, dictionary := range dictionaries {
		meta, exists := d.SelectionValues[dictionary].ValuesMeta[value]
		if !exists {
			continue
		}
		if found {
			if meta.SecondaryLabel != result.SecondaryLabel {
				result.SecondaryLabel = ""
			}
			if !slices.Equal(meta.Covers, result.Covers) {
				result.Covers = nil
			}
			if meta.Hex != result.Hex || meta.Swatch != result.Swatch {
				result.Hex, result.Swatch = "", ""
			}
		} else {
			result = meta
			found = true
		}
	}
	if len(dictionaries) > 1 {
		// Порядок одной сетки не сопоставим с порядком другой.
		result.Order = nil
	}
	return result
}
