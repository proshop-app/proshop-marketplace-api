package taxonomy

// DictionarySources собирает готовые подсказки для полей раздела и его потомков.
// Индексы общие для всего снимка; правила сворачиваются тем же способом, что в HintDictionaries.
func (d Definition) DictionarySources() map[string]map[string][]string {
	result := map[string]map[string][]string{}
	fields := d.Fields()
	index := d.dictionaryBindingIndex()
	for category, scope := range d.bindingScopes() {
		for code := range scope.attributes {
			attribute, exists := fields[code]
			if !exists || attribute.Type != "string" {
				continue
			}

			dictionaries := hintDictionaries(attribute, scope.path, index)
			if len(dictionaries) == 0 {
				continue
			}
			if result[category] == nil {
				result[category] = map[string][]string{}
			}
			result[category][code] = dictionaries
		}
	}
	return result
}
