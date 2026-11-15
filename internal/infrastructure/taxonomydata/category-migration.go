package taxonomydata

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// CategoryMigration делает разрыв кодов явной, проверяемой частью поставки.
type CategoryMigration struct {
	FromRelease int64             `yaml:"fromRelease"`
	ToRelease   int64             `yaml:"toRelease"`
	Mappings    []CategoryMapping `yaml:"mappings"`
}

// CategoryMapping задаёт все допустимые назначения; split требует разбора карточки.
type CategoryMapping struct {
	From   string   `yaml:"from"`
	To     []string `yaml:"to"`
	Action string   `yaml:"action"`
	Reason string   `yaml:"reason"`
}

func migrationFrom(source fs.FS, release taxonomy.Release) (CategoryMigration, error) {
	var migration CategoryMigration
	if _, err := fs.Stat(source, dataDirectory+"category-migration.yaml"); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return migration, nil
		}
		return migration, err
	}
	if err := decodeFile(source, "category-migration.yaml", &migration); err != nil {
		return migration, err
	}
	if migration.FromRelease < 1 || migration.ToRelease <= migration.FromRelease || len(migration.Mappings) == 0 {
		return migration, fmt.Errorf("%w: неверные версии миграции категорий", ErrInvalidBundle)
	}
	seen := map[string]bool{}
	leaves := map[string]bool{}
	collectMigrationLeaves(release.Categories, leaves)
	for _, mapping := range migration.Mappings {
		if mapping.From == "" || seen[mapping.From] || mapping.Reason == "" {
			return migration, fmt.Errorf("%w: повтор или пустая строка миграции", ErrInvalidBundle)
		}
		seen[mapping.From] = true
		valid := mapping.Action == "unchanged" && len(mapping.To) == 1 && mapping.To[0] == mapping.From ||
			(mapping.Action == "merge" || mapping.Action == "renamed") && len(mapping.To) == 1 ||
			mapping.Action == "split" && len(mapping.To) > 1 || mapping.Action == "deleted" && len(mapping.To) == 0
		if !valid {
			return migration, fmt.Errorf("%w: неверный вид переноса %s", ErrInvalidBundle, mapping.From)
		}
		targets := map[string]bool{}
		for _, id := range mapping.To {
			if !leaves[id] || targets[id] {
				return migration, fmt.Errorf("%w: неверный адресат переноса %s", ErrInvalidBundle, id)
			}
			targets[id] = true
		}
	}
	return migration, nil
}

func collectMigrationLeaves(categories []taxonomy.Category, leaves map[string]bool) {
	for _, category := range categories {
		if len(category.Children) == 0 {
			leaves[category.ID] = true
		}
		collectMigrationLeaves(category.Children, leaves)
	}
}
