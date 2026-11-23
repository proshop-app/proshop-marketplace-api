package searchapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const candidatesPath = "/api/v1/internal/catalog"

// Candidates использует отдельный токен чтения атрибутов каталога.
type Candidates struct{ index *Index }

// Candidates — чтение атрибутов каталога своим токеном через транспорт индекса:
// второй пул соединений к тому же поиску не нужен.
func (x *Index) Candidates(token string) (*Candidates, error) {
	if len(token) < minTokenLength {
		return nil, fmt.Errorf("некорректный токен чтения кандидатов поиска")
	}
	endpoint := *x
	endpoint.base = strings.TrimSuffix(x.base, apiPath) + candidatesPath
	endpoint.token = token
	return &Candidates{index: &endpoint}, nil
}

// AttributeValues читает страницу; сбои не повторяются до следующего обхода.
func (c *Candidates) AttributeValues(
	ctx context.Context,
	query ports.AttributeValuesQuery,
) (ports.AttributeValuesPage, error) {
	var page ports.AttributeValuesPage
	if query.Size < 1 || query.Size > 500 ||
		len(query.Categories) > ports.MaxCandidateCategories ||
		len(query.ExcludeCategories) > ports.MaxCandidateCategories {
		return page, ErrInvalid
	}
	values := candidateQueryValues(query)
	status, err := c.index.request(ctx, http.MethodGet, "/attribute-values?"+values.Encode(), nil, &page)
	if status == http.StatusBadRequest {
		return page, ErrInvalid
	}
	return page, err
}

func candidateQueryValues(query ports.AttributeValuesQuery) url.Values {
	values := url.Values{
		"key":   {query.Attribute},
		"size":  {strconv.Itoa(query.Size)},
		"after": {query.After},
	}
	for _, category := range query.Categories {
		values.Add("categoryId", category)
	}
	for _, category := range query.ExcludeCategories {
		values.Add("excludeCategoryId", category)
	}
	return values
}
