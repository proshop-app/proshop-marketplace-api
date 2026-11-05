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
	code string,
	size int,
	after string,
) (ports.AttributeValuesPage, error) {
	var page ports.AttributeValuesPage
	if size < 1 || size > 500 {
		return page, ErrInvalid
	}
	query := url.Values{"key": {code}, "size": {strconv.Itoa(size)}, "after": {after}}
	status, err := c.index.request(ctx, http.MethodGet, "/attribute-values?"+query.Encode(), nil, &page)
	if status == http.StatusBadRequest {
		return page, ErrInvalid
	}
	return page, err
}
