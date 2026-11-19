package platformapi

import (
	"context"
	"net/http"
	"net/url"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// SuggestCities использует общий справочник сервера без контекста магазина.
func (c *Client) SuggestCities(ctx context.Context, value string) ([]ports.CitySuggestion, error) {
	ctx, cancel := context.WithTimeout(ctx, c.http.Timeout)
	defer cancel()
	items := []ports.CitySuggestion{}
	err := c.call(ctx, http.MethodGet, "", "/delivery/cities?"+url.Values{"value": {value}}.Encode(), nil, &items)
	return items, err
}
