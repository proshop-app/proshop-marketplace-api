package searchapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// Candidates использует отдельный токен чтения атрибутов каталога.
type Candidates struct{ index *Index }

// NewCandidates проверяет настройки и сохраняет общий защищённый HTTP-транспорт.
func NewCandidates(cfg Config) (*Candidates, error) {
	index, err := New(cfg)
	if err != nil {
		return nil, err
	}
	index.base = strings.TrimRight(cfg.URL, "/") + "/api/v1/internal/catalog"
	return &Candidates{index: index}, nil
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
