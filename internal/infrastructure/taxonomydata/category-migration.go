package taxonomydata

import (
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// categoryMigration делает разрыв кодов явной, проверяемой частью поставки.
type categoryMigration struct {
	FromRelease int64             `json:"fromRelease"`
	ToRelease   int64             `json:"toRelease"`
	Mappings    []categoryMapping `json:"mappings"`
}

// categoryMapping задаёт все допустимые назначения; split требует разбора карточки.
type categoryMapping struct {
	From   string   `json:"from"`
	To     []string `json:"to"`
	Action string   `json:"action"`
	Reason string   `json:"reason"`
}

func validateCategoryMigration(migration categoryMigration, previous, next []taxonomy.Category) error {
	if migration.FromRelease < 1 || migration.ToRelease <= migration.FromRelease || len(migration.Mappings) == 0 {
		return fmt.Errorf("%w: неверные версии миграции категорий", ErrInvalidBundle)
	}
	sources := map[string]bool{}
	collectMigrationLeaves(previous, sources)
	leaves := map[string]bool{}
	collectMigrationLeaves(next, leaves)
	for _, mapping := range migration.Mappings {
		if !sources[mapping.From] || mapping.Reason == "" {
			return fmt.Errorf("%w: неверный исходный лист миграции %s", ErrInvalidBundle, mapping.From)
		}
		delete(sources, mapping.From)
		valid := mapping.Action == "unchanged" && len(mapping.To) == 1 && mapping.To[0] == mapping.From ||
			(mapping.Action == "merge" || mapping.Action == "renamed") && len(mapping.To) == 1 ||
			mapping.Action == "split" && len(mapping.To) > 1 || mapping.Action == "deleted" && len(mapping.To) == 0
		if !valid {
			return fmt.Errorf("%w: неверный вид переноса %s", ErrInvalidBundle, mapping.From)
		}
		targets := map[string]bool{}
		for _, id := range mapping.To {
			if !leaves[id] || targets[id] {
				return fmt.Errorf("%w: неверный адресат переноса %s", ErrInvalidBundle, id)
			}
			targets[id] = true
		}
	}
	if len(sources) != 0 {
		return fmt.Errorf("%w: не покрыты %d исходных листьев миграции", ErrInvalidBundle, len(sources))
	}

	return nil
}

func collectMigrationLeaves(categories []taxonomy.Category, leaves map[string]bool) {
	for _, category := range categories {
		if len(category.Children) == 0 {
			leaves[category.ID] = true
		}
		collectMigrationLeaves(category.Children, leaves)
	}
}
