package reference

import (
	"slices"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// candidateQueries не делит исключения: объединение таких запросов вернуло бы запрещённые ветки.
func candidateQueries(
	definition taxonomy.Definition,
	scope taxonomy.CandidateScope,
) ([]taxonomy.CandidateScope, []taxonomy.CandidateScope) {
	if len(scope.ExcludeCategories) <= ports.MaxCandidateCategories {
		if len(scope.Categories) <= ports.MaxCandidateCategories {
			return []taxonomy.CandidateScope{scope}, nil
		}
		queries := []taxonomy.CandidateScope{}
		for start := 0; start < len(scope.Categories); start += ports.MaxCandidateCategories {
			query := scope
			end := min(start+ports.MaxCandidateCategories, len(scope.Categories))
			query.Categories = slices.Clone(scope.Categories[start:end])
			queries = append(queries, query)
		}
		return queries, nil
	}
	roots := scope.Categories
	if len(roots) == 0 {
		for _, category := range definition.Categories {
			roots = append(roots, category.ID)
		}
	}
	queries := []taxonomy.CandidateScope{}
	skipped := []taxonomy.CandidateScope{}
	for _, root := range roots {
		if slices.Contains(scope.ExcludeCategories, root) {
			continue
		}
		query := scope
		query.Categories = []string{root}
		query.ExcludeCategories = []string{}
		for _, excluded := range scope.ExcludeCategories {
			_, path, found := definition.Category(excluded)
			if found && slices.Contains(path, root) {
				query.ExcludeCategories = append(query.ExcludeCategories, excluded)
			}
		}
		if len(query.ExcludeCategories) > ports.MaxCandidateCategories {
			skipped = append(skipped, query)
		} else {
			queries = append(queries, query)
		}
	}
	return queries, skipped
}
