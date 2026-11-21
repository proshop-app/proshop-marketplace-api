package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	lockSynonymDictionaries = `SELECT code FROM reference.dictionaries WHERE code = 'brand' OR code IN (
		SELECT e.dictionary_code FROM reference.search_synonym_registry_scopes s
		JOIN reference.entries e ON e.id = s.entry_id
	) ORDER BY code FOR SHARE`
	readSynonymInputs = `SELECT category_id, canonical_name, equivalent_name, canonical_name,
			'taxonomy_name', category_id, ''
		FROM reference.search_synonym_taxonomy_names
		UNION ALL
		SELECT s.category_id, s.category_name, a.text, e.canonical_name, 'registry_alias', a.id::text, e.revision::text
		FROM reference.search_synonym_registry_scopes s
		JOIN reference.entries e ON e.id = s.entry_id AND e.canonical_key = s.canonical_key
			AND e.status = 'confirmed' AND e.dictionary_code <> 'brand'
		JOIN reference.aliases a ON a.entry_id = e.id AND a.dictionary_code = e.dictionary_code
			AND a.status = 'active' AND a.kind IN ('spelling', 'translit')
		ORDER BY 5 DESC, 1, 3, 6 LIMIT 5001`
	readSynonymBrands = `SELECT a.key FROM reference.aliases a
		WHERE a.dictionary_code = 'brand' AND a.status = 'active' AND a.key = ANY($1)
		UNION
		SELECT e.canonical_key FROM reference.entries e
		WHERE e.dictionary_code = 'brand' AND e.canonical_key = ANY($1)`
)

// Inputs фиксирует версии словарей до конца транзакции. Таксономия блокируется первой,
// в том же порядке, в котором публикуется её снимок со значениями реестра.
func (r *searchSynonyms) Inputs(ctx context.Context) ([]ports.SynonymInput, error) {
	var locked []string
	if err := rawSQL(ctx, r.db, lockSynonymDictionaries).Scan(&locked).Error; err != nil {
		return nil, err
	}
	rows, err := rawSQL(ctx, r.db, readSynonymInputs).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	inputs := make([]ports.SynonymInput, 0)
	for rows.Next() {
		var i ports.SynonymInput
		err := rows.Scan(&i.Category, &i.CategoryName, &i.From, &i.To, &i.Source.Kind, &i.Source.ID, &i.Source.Revision)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, i)
	}
	return inputs, rows.Err()
}

func (r *searchSynonyms) Brands(ctx context.Context, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	var matches []string
	err := rawSQL(ctx, r.db, readSynonymBrands, keys).Scan(&matches).Error
	return matches, err
}
