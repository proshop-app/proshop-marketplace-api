package taxonomydata

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// WriteReview выводит все листья, сгруппированные по направлению и полной схеме.
// Редактор читает каждую группу: формальная проверка не доказывает применимость поля.
func WriteReview(output io.Writer, release taxonomy.Release, direction string) error {
	found := false
	for _, root := range release.Categories {
		if direction != "" && root.ID != direction {
			continue
		}
		found = true
		if _, err := fmt.Fprintf(output, "\n%s — %s\n", root.ID, root.Name); err != nil {
			return err
		}
		groups := map[string][]string{}
		collectReview(root.Children, groups)
		names := make([]string, 0, len(groups))
		for name := range groups {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			schema := release.Schemas[name]
			optional := map[string]bool{}
			for _, code := range schema.Optional {
				optional[code] = true
			}
			fields := make([]string, 0, len(schema.Attributes))
			for _, code := range schema.Attributes {
				if !optional[code] {
					code += "!"
				}
				fields = append(fields, code)
			}
			_, err := fmt.Fprintf(
				output,
				"%s [%s]\n  %s\n",
				name,
				strings.Join(fields, ", "),
				strings.Join(groups[name], "; "),
			)
			if err != nil {
				return err
			}
		}
	}
	if !found {
		return taxonomy.ErrInvalid.WithMessage("направление отчёта не найдено")
	}
	return nil
}

func collectReview(categories []taxonomy.Category, groups map[string][]string) {
	for _, category := range categories {
		if len(category.Children) == 0 {
			groups[category.Schema] = append(groups[category.Schema], category.Name+" ("+category.ID+")")
		}
		collectReview(category.Children, groups)
	}
}
