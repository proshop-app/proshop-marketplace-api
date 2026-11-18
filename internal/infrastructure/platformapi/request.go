package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (c *Client) newRequest(
	ctx context.Context,
	method string,
	tenant ids.TenantID,
	path string,
	payload any,
) (*http.Request, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("запрос к серверу: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := c.baseURL + "/api/v1/internal/marketplace"
	if tenant != "" {
		endpoint += "/tenants/" + url.PathEscape(string(tenant))
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint+path, body)
	if err != nil {
		return nil, fmt.Errorf("запрос к серверу: %w", err)
	}
	request.Header.Set(HeaderToken, c.token)
	request.Header.Set("Accept", "application/json")
	if deadline, ok := ctx.Deadline(); ok {
		request.Header.Set(HeaderDeadline, strconv.FormatInt(deadline.UnixMilli(), 10))
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request, nil
}

func decodeResponse(body io.Reader, out any) error {
	raw, err := io.ReadAll(io.LimitReader(body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return ErrInvalidResponse
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return ErrInvalidResponse.Wrap(err)
	}
	return nil
}
