package searchapi

import "github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"

func attributeSearchTerms(attributes []attribute) (map[string][]string, error) {
	var out map[string][]string
	for _, attribute := range attributes {
		if err := taxonomy.ValidateSearchTerms(attribute.SearchTerms); err != nil {
			return nil, ErrUnavailable.Wrap(err)
		}
		if len(attribute.SearchTerms) == 0 {
			continue
		}
		if out == nil {
			out = make(map[string][]string)
		}
		out[attribute.Key] = append([]string(nil), attribute.SearchTerms...)
	}
	return out, nil
}
