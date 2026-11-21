// Package taxonomydata проверяет нормализованный комплект до подключения к БД.
package taxonomydata

import (
	"errors"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// ErrInvalidBundle отделяет ошибки файлов поставки от ошибок структуры домена.
var ErrInvalidBundle = errors.New("некорректный комплект данных каталога")

// Coverage проверяет полноту привязок поставки.
type Coverage struct {
	Directions   int `json:"directions"`
	Categories   int `json:"categories"`
	Leaves       int `json:"leaves"`
	Schemas      int `json:"schemas"`
	Attributes   int `json:"attributes"`
	Dictionaries int `json:"dictionaries"`
}

func checkCoverage(release taxonomy.Release) (Coverage, error) {
	var coverage Coverage
	if err := taxonomy.ValidateRelease(release); err != nil {
		return coverage, err
	}
	if err := auditRelease(release); err != nil {
		return coverage, err
	}
	for _, root := range release.Categories {
		if !taxonomy.ValidIcon(root.Icon) {
			return coverage, fmt.Errorf("категория %s: %w", root.ID, ErrInvalidBundle)
		}
	}
	coverage.Directions = len(release.Categories)
	coverage.Attributes = len(release.Attributes)
	coverage.Schemas = len(release.Schemas)
	usedSchemas := map[string]bool{}
	usedFields := map[string]bool{}
	var walk func([]taxonomy.Category)
	walk = func(categories []taxonomy.Category) {
		for _, category := range categories {
			coverage.Categories++
			if len(category.Children) == 0 {
				coverage.Leaves++
				usedSchemas[category.Schema] = true
			}
			walk(category.Children)
		}
	}
	walk(release.Categories)
	for _, schema := range release.Schemas {
		for _, code := range schema.Attributes {
			usedFields[code] = true
		}
	}
	for _, a := range release.Attributes {
		if a.Dictionary != "" {
			coverage.Dictionaries++
		}
		// Исторический код сохраняется глобально, но не навязывается новым карточкам.
		if a.Deprecated {
			usedFields[a.Code] = true
		}
	}
	for name := range release.Schemas {
		if !usedSchemas[name] && sequentialImportSchemas[name] == "" {
			return coverage, fmt.Errorf("%w: неиспользуемая схема %s", ErrInvalidBundle, name)
		}
	}
	if len(usedFields) != coverage.Attributes {
		return coverage, ErrInvalidBundle
	}
	return coverage, nil
}
