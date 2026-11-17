package taxonomy

// Checker компилирует неизменяемое определение на время одной операции.
// Владение межтранзакционным снимком остаётся у UnitOfWork.
type Checker struct {
	version    int64
	categories map[string]checkContext
}

type checkContext struct {
	Category CategorySchema
	Path     []string
	Policy   string
}

// Checker строит схемы и пути один раз для всех карточек страницы.
func (d Definition) Checker() Checker {
	result := Checker{version: d.Version, categories: map[string]checkContext{}}
	fields := d.Fields()
	var walk func([]Category, []string, string)
	walk = func(nodes []Category, path []string, inherited string) {
		for _, node := range nodes {
			policy := inheritModerationPolicy(inherited, node.Moderation)
			children := node.Children
			node.Children = nil
			current := append(append([]string{}, path...), node.ID)
			result.categories[node.ID] = checkContext{
				Category: categorySchema(node, fields, d.Schemas[node.Schema]),
				Path:     current,
				Policy:   policy,
			}
			walk(children, current, policy)
		}
	}
	walk(d.Categories, nil, "auto")
	return result
}

// Check использует ту же проверку, что Definition.Check.
func (c Checker) Check(card *Card) Publication {
	var scope checkContext
	found := false
	if card != nil {
		scope, found = c.categories[card.CategoryID]
	}
	return checkCategory(c.version, scope.Category, scope.Path, found, card)
}

// ModerationPolicy соответствует наследованию Definition.ModerationPolicy.
func (c Checker) ModerationPolicy(category string) string {
	if scope, ok := c.categories[category]; ok {
		return scope.Policy
	}
	return "auto"
}
