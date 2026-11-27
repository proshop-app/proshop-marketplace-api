package taxonomy

import (
	"slices"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

// CandidateScope — область с неизменным упорядоченным набором источников.
// Пустые Categories означают весь ассортимент; исключения отсекают целые поддеревья.
type CandidateScope struct {
	Attribute         string
	Categories        []string
	ExcludeCategories []string
	Dictionaries      []string
}

// CandidateScopes использует ту же свёртку, что и HintDictionaries.
// При смене набора, включая extend, начинается отдельная область: у неё другой
// получатель кандидатов или набор словарей для проверки занятости ключей.
func (d Definition) CandidateScopes(code string, kind reference.Kind) []CandidateScope {
	result := []CandidateScope{}
	if d.Version == 0 || kind != reference.KindOpen {
		return result
	}
	index := d.dictionaryBindingIndex()
	for _, attribute := range d.Attributes {
		if attribute.Type != "string" {
			continue
		}
		for _, scope := range d.attributeCandidateScopes(attribute, index) {
			if slices.Contains(scope.Dictionaries, code) {
				result = append(result, scope)
			}
		}
	}
	return result
}

// CandidateScopesByReceiver раскладывает области всех атрибутов по словарю-получателю
// кандидатов (последнему в наборе) за один обход дерева. Проход сбора берёт области
// каждого словаря отсюда: пересчитывать их на каждый словарь — обход всего дерева по
// всем атрибутам столько раз, сколько словарей.
func (d Definition) CandidateScopesByReceiver() map[string][]CandidateScope {
	result := map[string][]CandidateScope{}
	if d.Version == 0 {
		return result
	}
	index := d.dictionaryBindingIndex()
	for _, attribute := range d.Attributes {
		if attribute.Type != "string" {
			continue
		}
		for _, scope := range d.attributeCandidateScopes(attribute, index) {
			if len(scope.Dictionaries) == 0 {
				continue
			}
			receiver := scope.Dictionaries[len(scope.Dictionaries)-1]
			result[receiver] = append(result[receiver], scope)
		}
	}
	return result
}

func (d Definition) attributeCandidateScopes(
	attribute Attribute,
	index map[dictionaryBindingKey]DictionaryBinding,
) []CandidateScope {
	global := hintDictionaries(attribute, nil, index)
	scopes := []CandidateScope{{
		Attribute:         attribute.Code,
		Categories:        []string{},
		ExcludeCategories: []string{},
		Dictionaries:      global,
	}}
	var walk func(Category, []string, []string, int)
	walk = func(category Category, ancestors, inherited []string, parent int) {
		path := append(slices.Clone(ancestors), category.ID)
		current := hintDictionaries(attribute, path, index)
		if !slices.Equal(current, inherited) {
			scopes[parent].ExcludeCategories = append(scopes[parent].ExcludeCategories, category.ID)
			parent = len(scopes)
			scopes = append(scopes, CandidateScope{
				Attribute:         attribute.Code,
				Categories:        []string{category.ID},
				ExcludeCategories: []string{},
				Dictionaries:      current,
			})
		}
		for _, child := range category.Children {
			walk(child, path, current, parent)
		}
	}
	for _, category := range d.Categories {
		walk(category, nil, global, 0)
	}
	return scopes
}
