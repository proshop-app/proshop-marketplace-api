package taxonomy

// DefaultModerationPolicy применяется при отсутствии явной политики.
const DefaultModerationPolicy = "auto"

// ModerationPolicy наследует ближайшую явно заданную политику узла.
func (d Definition) ModerationPolicy(id string) string {
	var walk func([]Category, string) (string, bool)
	walk = func(nodes []Category, inherited string) (string, bool) {
		for _, node := range nodes {
			policy := inherited
			if node.Moderation != "" {
				policy = node.Moderation
			}
			if node.ID == id {
				return policy, true
			}
			if result, found := walk(node.Children, policy); found {
				return result, true
			}
		}
		return DefaultModerationPolicy, false
	}
	policy, _ := walk(d.Categories, DefaultModerationPolicy)
	return policy
}
