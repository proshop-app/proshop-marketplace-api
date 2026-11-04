package referenceapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// Resolve нужен только наблюдаемому modelKey; прежний срок 300 мс задаёт вызывающий сценарий.
func (c *Client) Resolve(ctx context.Context, text string) (string, bool, error) {
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "", false, err
	}
	response, err := c.Do(ctx, ports.ReferenceRequest{
		Method:     http.MethodPost,
		Dictionary: "brand",
		Resource:   "resolve",
		Body:       body,
	})
	if err != nil || response.Status != http.StatusOK {
		return "", false, ErrUnavailable
	}
	var resolution struct {
		SchemaVersion int    `json:"schemaVersion"`
		Dictionary    string `json:"dictionary"`
		Match         *struct {
			EntryID string `json:"entryId"`
		} `json:"match"`
	}
	if json.Unmarshal(response.Body, &resolution) != nil || resolution.SchemaVersion != 1 ||
		resolution.Dictionary != "brand" {
		return "", false, ErrUnavailable
	}
	if resolution.Match == nil {
		return "", false, nil
	}
	if resolution.Match.EntryID == "" {
		return "", false, ErrUnavailable
	}
	return resolution.Match.EntryID, true, nil
}

// Values читает полный словарь со стабильными id и порядком.
func (c *Client) Values(ctx context.Context, dictionary string) ([]ports.ReferenceValue, error) {
	response, err := c.Do(ctx, ports.ReferenceRequest{
		Method:     http.MethodGet,
		Dictionary: dictionary,
		Resource:   "values",
	})
	if err != nil || response.Status != http.StatusOK {
		return nil, ErrUnavailable
	}
	var page struct {
		SchemaVersion int                    `json:"schemaVersion"`
		Dictionary    string                 `json:"dictionary"`
		Kind          string                 `json:"kind"`
		Items         []ports.ReferenceValue `json:"items"`
	}
	if json.Unmarshal(response.Body, &page) != nil || page.SchemaVersion != 1 || page.Dictionary != dictionary ||
		page.Kind != "closed" || page.Items == nil || len(page.Items) == 0 {
		return nil, ErrUnavailable
	}
	seen := map[string]bool{}
	for _, item := range page.Items {
		if item.ID == "" || item.Value == "" || seen[item.ID] {
			return nil, ErrUnavailable
		}
		seen[item.ID] = true
	}
	return page.Items, nil
}
